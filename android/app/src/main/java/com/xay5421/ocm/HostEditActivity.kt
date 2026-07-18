package com.xay5421.ocm

import android.os.Bundle
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import com.google.android.material.appbar.MaterialToolbar
import com.google.android.material.button.MaterialButton
import com.google.android.material.textfield.TextInputEditText

class HostEditActivity : AppCompatActivity() {

    private lateinit var store: HostStore
    private var oldName: String? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_host_edit)

        store = HostStore(this)
        oldName = intent.getStringExtra("name")
        val existing = oldName?.let { store.get(it) }

        findViewById<MaterialToolbar>(R.id.toolbar).title =
            if (existing == null) "添加主机" else "编辑 ${existing.name}"

        if (existing != null) {
            et(R.id.etName).setText(existing.name)
            et(R.id.etHost).setText(existing.host)
            et(R.id.etPort).setText(existing.port.toString())
            et(R.id.etUser).setText(existing.user)
            et(R.id.etRemotePort).setText(existing.remotePort.toString())
            et(R.id.etDirectory).setText(existing.directory)
            et(R.id.etAuthUser).setText(existing.authUser)
            et(R.id.etAuthPass).setText(existing.authPass)
        } else {
            et(R.id.etPort).setText("22")
            et(R.id.etRemotePort).setText("4096")
        }

        findViewById<MaterialButton>(R.id.btnSave).setOnClickListener { save() }
    }

    private fun et(id: Int): TextInputEditText = findViewById(id)

    private fun save() {
        val name = et(R.id.etName).text.toString().trim()
        val host = et(R.id.etHost).text.toString().trim()
        val user = et(R.id.etUser).text.toString().trim()
        if (name.isEmpty() || host.isEmpty() || user.isEmpty()) {
            Toast.makeText(this, "名称/主机/用户必填", Toast.LENGTH_SHORT).show()
            return
        }
        val h = Host(
            name = name,
            host = host,
            port = et(R.id.etPort).text.toString().toIntOrNull() ?: 22,
            user = user,
            remotePort = et(R.id.etRemotePort).text.toString().toIntOrNull() ?: 4096,
            authUser = et(R.id.etAuthUser).text.toString(),
            authPass = et(R.id.etAuthPass).text.toString(),
            directory = et(R.id.etDirectory).text.toString().trim(),
        )
        store.upsert(h, oldName)
        finish()
    }
}
