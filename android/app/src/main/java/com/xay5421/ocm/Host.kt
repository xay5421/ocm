package com.xay5421.ocm

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject

data class Host(
    val name: String,
    val host: String,
    val port: Int,
    val user: String,
    val remotePort: Int,
    val authUser: String,
    val authPass: String,
    val directory: String = "",
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("name", name)
        put("host", host)
        put("port", port)
        put("user", user)
        put("remote_port", remotePort)
        put("auth_user", authUser)
        put("auth_pass", authPass)
        put("directory", directory)
    }

    companion object {
        fun fromJson(o: JSONObject): Host = Host(
            name = o.getString("name"),
            host = o.getString("host"),
            port = o.optInt("port", 22),
            user = o.getString("user"),
            remotePort = o.optInt("remote_port", 4096),
            authUser = o.optString("auth_user", ""),
            authPass = o.optString("auth_pass", ""),
            directory = o.optString("directory", ""),
        )
    }
}

class HostStore(private val context: Context) {
    private val prefs = context.getSharedPreferences("hosts", Context.MODE_PRIVATE)

    fun list(): List<Host> {
        val arr = JSONArray(prefs.getString("hosts", "[]"))
        return (0 until arr.length()).map { Host.fromJson(arr.getJSONObject(it)) }
    }

    fun get(name: String): Host? = list().find { it.name == name }

    fun upsert(host: Host, oldName: String? = null) {
        val remaining = list().filter { it.name != host.name && it.name != oldName }
        save(remaining + host)
    }

    fun delete(name: String) {
        save(list().filter { it.name != name })
    }

    /**
     * Pretty-printed JSON for copy-paste transfer. Includes the ed25519
     * key seed so a reinstall can restore the same SSH identity.
     */
    fun exportJson(): String {
        val arr = JSONArray()
        list().forEach { arr.put(it.toJson()) }
        return JSONObject().apply {
            put("key_seed", KeyManager.exportSeedBase64(context))
            put("hosts", arr)
        }.toString(2)
    }

    /**
     * Import from JSON: either a bare hosts array (legacy) or an object
     * {"key_seed": ..., "hosts": [...]}. Existing hosts with the same name
     * are overwritten; others are kept. Returns imported host count.
     */
    fun importJson(text: String): Int {
        val t = text.trim()
        val arr: JSONArray
        if (t.startsWith("{")) {
            val o = JSONObject(t)
            arr = o.optJSONArray("hosts") ?: JSONArray()
            val seed = o.optString("key_seed", "")
            if (seed.isNotEmpty()) KeyManager.importSeedBase64(context, seed)
        } else {
            arr = JSONArray(t)
        }
        val imported = (0 until arr.length()).map { Host.fromJson(arr.getJSONObject(it)) }
        val importedNames = imported.map { it.name }.toSet()
        save(list().filter { it.name !in importedNames } + imported)
        return imported.size
    }

    private fun save(hosts: List<Host>) {
        val arr = JSONArray()
        hosts.sortedBy { it.name }.forEach { arr.put(it.toJson()) }
        prefs.edit().putString("hosts", arr.toString()).apply()
    }
}
