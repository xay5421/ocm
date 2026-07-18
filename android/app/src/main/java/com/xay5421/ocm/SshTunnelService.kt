package com.xay5421.ocm

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import net.schmizz.sshj.SSHClient
import net.schmizz.sshj.connection.channel.direct.Parameters
import net.schmizz.sshj.transport.verification.PromiscuousVerifier
import net.schmizz.sshj.userauth.keyprovider.KeyPairWrapper
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket

/**
 * Foreground service holding one SSH connection with a local port forward
 * to the remote opencode serve (remote 127.0.0.1:<remotePort>).
 */
class SshTunnelService : Service() {

    companion object {
        const val ACTION_CONNECT = "com.xay5421.ocm.CONNECT"
        const val ACTION_STOP = "com.xay5421.ocm.STOP"

        const val STATE_IDLE = "idle"
        const val STATE_CONNECTING = "connecting"
        const val STATE_RUNNING = "running"
        const val STATE_ERROR = "error"

        @Volatile var state: String = STATE_IDLE
            private set
        @Volatile var localPort: Int = 0
            private set
        @Volatile var lastError: String = ""
            private set
        @Volatile var currentHost: String = ""
            private set
    }

    private var client: SSHClient? = null
    private var serverSocket: ServerSocket? = null
    private var worker: Thread? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_CONNECT -> {
                val name = intent.getStringExtra("name") ?: return START_NOT_STICKY
                val host = HostStore(this).get(name) ?: return START_NOT_STICKY
                startInForeground("连接 ${host.name} …")
                disconnect()
                connect(host)
            }
            ACTION_STOP -> {
                disconnect()
                state = STATE_IDLE
                currentHost = ""
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
            }
        }
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        disconnect()
        state = STATE_IDLE
        super.onDestroy()
    }

    private fun connect(host: Host) {
        // Defensive: ensure the full BouncyCastle provider is installed even
        // if Application.onCreate did not run in this process.
        App.installFullBouncyCastle()
        state = STATE_CONNECTING
        currentHost = host.name
        lastError = ""
        worker = Thread {
            try {
                val ssh = SSHClient()
                client = ssh
                ssh.addHostKeyVerifier(PromiscuousVerifier())
                ssh.connectTimeout = 15000
                // No read timeout: this is a long-lived tunnel; a 30s SO
                // timeout races the 30s keepalive and kills idle sessions.
                ssh.timeout = 0
                ssh.connect(host.host, host.port)
                ssh.authPublickey(host.user, KeyPairWrapper(KeyManager.keyPair(this)))
                ssh.connection.keepAlive.keepAliveInterval = 30

                // Auto-start opencode serve if it is not running (POSIX
                // remotes only). Failure is recorded but not fatal: the
                // WebView will surface it if the serve is really down.
                updateNotification("${host.name}: 检查 opencode serve …")
                try {
                    SshOps.ensureServe(ssh, host)
                } catch (e: Exception) {
                    lastError = "serve 拉起失败：${e.message}"
                }

                val ss = ServerSocket()
                serverSocket = ss
                ss.reuseAddress = true
                // NOT getLoopbackAddress(): on Android that is ::1 (IPv6),
                // but the WebView loads http://127.0.0.1 (IPv4 only).
                // Stable per-host port: the WebView's localStorage/cookies are
                // scoped to host:port, so a fixed port lets the web UI
                // remember the chosen project across connections.
                val loop = InetAddress.getByName("127.0.0.1")
                val preferred = 15000 + (host.name.hashCode().let { if (it < 0) -it else it } % 1000)
                try {
                    ss.bind(InetSocketAddress(loop, preferred))
                } catch (e: Exception) {
                    ss.bind(InetSocketAddress(loop, 0)) // port taken: fall back
                }
                localPort = ss.localPort

                val params = Parameters("127.0.0.1", localPort, "127.0.0.1", host.remotePort)
                state = STATE_RUNNING
                updateNotification("${host.name}: 127.0.0.1:$localPort → :${host.remotePort}")
                // listen() dies if a single channel open fails; keep rebuilding
                // the forwarder while the connection and server socket live.
                while (!ss.isClosed && ssh.isConnected) {
                    try {
                        ssh.newLocalPortForwarder(params, ss).listen() // blocks
                    } catch (e: Exception) {
                        if (ss.isClosed || !ssh.isConnected) break
                        lastError = describe(e)
                        Thread.sleep(300)
                    }
                }
            } catch (e: Exception) {
                if (state != STATE_IDLE) {
                    lastError = describe(e)
                    state = STATE_ERROR
                }
            }
        }
        worker!!.start()
    }

    private fun describe(e: Throwable): String =
        generateSequence(e as Throwable?) { it.cause }
            .joinToString(" ← ") { "${it.javaClass.simpleName}: ${it.message ?: ""}" }

    private fun disconnect() {
        try { serverSocket?.close() } catch (_: Exception) {}
        serverSocket = null
        val c = client
        client = null
        Thread {
            try { c?.disconnect() } catch (_: Exception) {}
        }.start()
        localPort = 0
    }

    private fun startInForeground(text: String) {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(
                NotificationChannel("tunnel", "SSH 隧道", NotificationManager.IMPORTANCE_LOW)
            )
        }
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(1, buildNotification(text), ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(1, buildNotification(text))
        }
    }

    private fun updateNotification(text: String) {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(1, buildNotification(text))
    }

    private fun buildNotification(text: String): Notification {
        val builder = if (Build.VERSION.SDK_INT >= 26) {
            Notification.Builder(this, "tunnel")
        } else {
            @Suppress("DEPRECATION") Notification.Builder(this)
        }
        return builder
            .setSmallIcon(android.R.drawable.stat_notify_sync)
            .setContentTitle("ocm 隧道")
            .setContentText(text)
            .setOngoing(true)
            .build()
    }
}
