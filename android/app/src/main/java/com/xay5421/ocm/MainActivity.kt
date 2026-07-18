package com.xay5421.ocm

import android.Manifest
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.view.LayoutInflater
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import com.google.android.material.floatingactionbutton.FloatingActionButton

class MainActivity : AppCompatActivity() {

    private lateinit var store: HostStore
    private lateinit var adapter: HostAdapter
    private lateinit var emptyView: TextView
    private var hosts: List<Host> = emptyList()
    private val statuses = HashMap<String, SshOps.Status>()
    private val probing = java.util.Collections.synchronizedSet(HashSet<String>())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        setSupportActionBar(findViewById(R.id.toolbar))
        supportActionBar?.subtitle = "v${BuildConfig.VERSION_NAME} · ${BuildConfig.BUILD_TIME}"

        store = HostStore(this)
        KeyManager.ensureKey(this)

        if (Build.VERSION.SDK_INT >= 33) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }

        emptyView = findViewById(R.id.empty)
        val rv = findViewById<RecyclerView>(R.id.hostList)
        rv.layoutManager = LinearLayoutManager(this)
        adapter = HostAdapter()
        rv.adapter = adapter

        findViewById<FloatingActionButton>(R.id.fab).setOnClickListener {
            startActivity(Intent(this, HostEditActivity::class.java))
        }
    }

    override fun onResume() {
        super.onResume()
        refresh()
    }

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        menuInflater.inflate(R.menu.menu_main, menu)
        return true
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean = when (item.itemId) {
        R.id.action_pubkey -> { showPublicKey(); true }
        R.id.action_config -> { showImportExport(); true }
        else -> super.onOptionsItemSelected(item)
    }

    private fun refresh() {
        hosts = store.list()
        adapter.notifyDataSetChanged()
        emptyView.visibility = if (hosts.isEmpty()) View.VISIBLE else View.GONE
        probeAll()
    }

    private fun probeAll() {
        hosts.forEach { h ->
            if (!probing.add(h.name)) return@forEach
            Thread {
                val st = SshOps.probe(this, h)
                runOnUiThread {
                    probing.remove(h.name)
                    statuses[h.name] = st
                    if (!isDestroyed) adapter.notifyDataSetChanged()
                }
            }.start()
        }
    }

    private fun showHostMenu(h: Host) {
        AlertDialog.Builder(this)
            .setTitle(h.name)
            .setItems(arrayOf("编辑", "重启 serve", "升级 opencode", "删除")) { _, which ->
                when (which) {
                    0 -> startActivity(
                        Intent(this, HostEditActivity::class.java).putExtra("name", h.name)
                    )
                    1 -> runHostAction(h, "重启 serve") { SshOps.restartServe(this, h) }
                    2 -> runHostAction(h, "升级 opencode") { SshOps.upgrade(this, h) }
                    3 -> AlertDialog.Builder(this)
                        .setMessage("删除主机 ${h.name}？")
                        .setPositiveButton("删除") { _, _ -> store.delete(h.name); refresh() }
                        .setNegativeButton("取消", null)
                        .show()
                }
            }
            .show()
    }

    /** Runs a blocking host action on a worker thread with progress dialog. */
    private fun runHostAction(h: Host, title: String, action: () -> String) {
        val progress = AlertDialog.Builder(this)
            .setTitle("$title：${h.name}")
            .setMessage("执行中，请稍候…")
            .setCancelable(false)
            .show()
        Thread {
            val result = try {
                action()
            } catch (e: Exception) {
                "失败：${e.message ?: e.javaClass.simpleName}"
            }
            runOnUiThread {
                progress.dismiss()
                if (isDestroyed) return@runOnUiThread
                AlertDialog.Builder(this)
                    .setTitle("$title：${h.name}")
                    .setMessage(result)
                    .setPositiveButton("关闭", null)
                    .show()
                refresh()
            }
        }.start()
    }

    private fun showImportExport() {
        val et = android.widget.EditText(this).apply {
            setText(store.exportJson())
            textSize = 12f
            setHorizontallyScrolling(false)
            maxLines = 20
        }
        AlertDialog.Builder(this)
            .setTitle("配置导入/导出（JSON）")
            .setView(android.widget.ScrollView(this).apply {
                setPadding(32, 16, 32, 16)
                addView(et)
            })
            .setPositiveButton("导入") { _, _ ->
                try {
                    val n = store.importJson(et.text.toString())
                    refresh()
                    Toast.makeText(this, "已导入 $n 台主机", Toast.LENGTH_SHORT).show()
                } catch (e: Exception) {
                    Toast.makeText(this, "JSON 解析失败：${e.message}", Toast.LENGTH_LONG).show()
                }
            }
            .setNeutralButton("复制") { _, _ ->
                val cm = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                cm.setPrimaryClip(ClipData.newPlainText("ocm-hosts", store.exportJson()))
                Toast.makeText(this, "已复制", Toast.LENGTH_SHORT).show()
            }
            .setNegativeButton("取消", null)
            .show()
    }

    private fun showPublicKey() {
        val pub = KeyManager.publicKeyOpenSsh(this)
        AlertDialog.Builder(this)
            .setTitle("本机公钥")
            .setMessage(pub + "\n\n把这一行加到目标机的 ~/.ssh/authorized_keys")
            .setPositiveButton("复制") { _, _ ->
                val cm = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                cm.setPrimaryClip(ClipData.newPlainText("pubkey", pub))
                Toast.makeText(this, "已复制", Toast.LENGTH_SHORT).show()
            }
            .setNegativeButton("关闭", null)
            .show()
    }

    private inner class HostAdapter : RecyclerView.Adapter<HostAdapter.VH>() {

        inner class VH(v: View) : RecyclerView.ViewHolder(v) {
            val name: TextView = v.findViewById(R.id.hostName)
            val detail: TextView = v.findViewById(R.id.hostDetail)
            val status: TextView = v.findViewById(R.id.hostStatus)
        }

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): VH =
            VH(LayoutInflater.from(parent.context).inflate(R.layout.item_host, parent, false))

        override fun getItemCount(): Int = hosts.size

        override fun onBindViewHolder(holder: VH, position: Int) {
            val h = hosts[position]
            holder.name.text = h.name
            holder.detail.text = "${h.user}@${h.host}:${h.port}  →  :${h.remotePort}"
            val st = statuses[h.name]
            holder.status.text = if (st == null && probing.contains(h.name)) "…" else "●"
            holder.status.setTextColor(
                when (st) {
                    SshOps.Status.RUNNING -> 0xFF3FB950.toInt() // green: serve up
                    SshOps.Status.NO_SERVE -> 0xFFF0A020.toInt() // orange: ssh ok, serve down
                    SshOps.Status.OFFLINE -> 0xFFE05252.toInt() // red: ssh unreachable
                    null -> 0xFF9E9E9E.toInt()
                }
            )
            holder.itemView.setOnClickListener {
                startActivity(
                    Intent(this@MainActivity, WebViewActivity::class.java).putExtra("name", h.name)
                )
            }
            holder.itemView.setOnLongClickListener { showHostMenu(h); true }
        }
    }
}
