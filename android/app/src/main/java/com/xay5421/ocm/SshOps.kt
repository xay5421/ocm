package com.xay5421.ocm

import android.content.Context
import net.schmizz.sshj.SSHClient
import net.schmizz.sshj.transport.verification.PromiscuousVerifier
import net.schmizz.sshj.userauth.keyprovider.KeyPairWrapper
import java.util.concurrent.TimeUnit

/**
 * Short-lived SSH operations against a host: health probe, serve
 * start/restart, opencode upgrade. Mirrors ocm's manager.go commands.
 */
object SshOps {

    const val OPENCODE_BIN = "\"\$HOME/.opencode/bin/opencode\""

    enum class Status { OFFLINE, NO_SERVE, RUNNING }

    fun open(context: Context, host: Host, timeoutMs: Int = 10000): SSHClient {
        App.installFullBouncyCastle()
        val ssh = SSHClient()
        ssh.addHostKeyVerifier(PromiscuousVerifier())
        ssh.connectTimeout = timeoutMs
        ssh.timeout = 15000
        ssh.connect(host.host, host.port)
        ssh.authPublickey(host.user, KeyPairWrapper(KeyManager.keyPair(context)))
        return ssh
    }

    /** Runs a command, returns stdout+stderr. stdin is written then closed. */
    fun exec(ssh: SSHClient, command: String, stdin: String = "", timeoutSec: Long = 30): String {
        ssh.startSession().use { session ->
            val cmd = session.exec(command)
            cmd.outputStream.use { if (stdin.isNotEmpty()) it.write(stdin.toByteArray()) }
            val out = cmd.inputStream.readBytes().toString(Charsets.UTF_8)
            val err = cmd.errorStream.readBytes().toString(Charsets.UTF_8)
            cmd.join(timeoutSec, TimeUnit.SECONDS)
            return out + err
        }
    }

    private fun healthCmd(port: Int): String =
        "curl -s --noproxy '*' -o /dev/null -m 3 -w '%{http_code}' " +
            "http://127.0.0.1:$port/global/health 2>/dev/null"

    /** True when the health probe output means "serve is up" (200/401). */
    private fun isUp(out: String): Boolean =
        out.contains("200") || out.contains("401")

    /** Probe a host: ssh reachable? serve running? */
    fun probe(context: Context, host: Host): Status = try {
        open(context, host, 6000).use { ssh ->
            val out = exec(ssh, healthCmd(host.remotePort), timeoutSec = 10)
            if (isUp(out)) Status.RUNNING else Status.NO_SERVE
        }
    } catch (e: Exception) {
        Status.OFFLINE
    }

    /**
     * Ensure `opencode serve` runs on the remote (same logic as ocm's
     * StartServe: check health first, feed the password via stdin). Waits
     * until healthy. Throws with detail on failure. No-op if already up.
     */
    fun ensureServe(ssh: SSHClient, host: Host, waitSec: Int = 25) {
        val port = host.remotePort
        val startCmd = "IFS= read -r OCM_PW; " +
            "code=\$(${healthCmd(port)}); " +
            "if [ \"\$code\" != 200 ] && [ \"\$code\" != 401 ]; then " +
            "if [ -n \"\$OCM_PW\" ]; then export OPENCODE_SERVER_PASSWORD=\"\$OCM_PW\"; fi; " +
            "nohup $OPENCODE_BIN serve --port $port --hostname 127.0.0.1 " +
            ">>\"\$HOME/.opencode-serve.log\" 2>&1 </dev/null & fi"
        exec(ssh, startCmd, stdin = host.authPass + "\n", timeoutSec = 20)
        for (i in 0 until waitSec) {
            if (isUp(exec(ssh, healthCmd(port), timeoutSec = 10))) return
            Thread.sleep(1000)
        }
        throw IllegalStateException(
            "opencode serve 未能在 ${waitSec}s 内就绪（看远端 ~/.opencode-serve.log）")
    }

    /** Kill serve, start a fresh one, wait healthy. Returns a summary. */
    fun restartServe(context: Context, host: Host): String {
        open(context, host).use { ssh ->
            val port = host.remotePort
            exec(ssh, "pkill -f \"[o]pencode serve --port $port" +
                "|[s]erve --port $port --hostname 127.0.0.1\" || true")
            for (i in 0 until 10) {
                if (!isUp(exec(ssh, healthCmd(port), timeoutSec = 10))) break
                Thread.sleep(1000)
            }
            ensureServe(ssh, host)
            return "serve 已重启（端口 $port）"
        }
    }

    /** `opencode upgrade` + version, like ocm's UpgradeOpencode. */
    fun upgrade(context: Context, host: Host): String {
        open(context, host).use { ssh ->
            return exec(
                ssh,
                "$OPENCODE_BIN upgrade 2>&1 && printf 'version: ' && $OPENCODE_BIN --version 2>&1",
                timeoutSec = 180,
            ).trim().ifEmpty { "（无输出）" }
        }
    }
}
