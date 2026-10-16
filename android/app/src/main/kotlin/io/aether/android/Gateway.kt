package io.aether.android

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import java.io.File
import java.io.IOException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** What `aether gui --json` prints once it is serving. */
data class Handshake(val url: String, val addr: String)

// The CLI prints exactly {"url":"http://127.0.0.1:PORT/?token=TOKEN","addr":"127.0.0.1:PORT"}
// with json.Marshal. org.json is a stub in the unit tests' android.jar, and a
// general parser would accept values this shell must not follow, so the line
// is matched against that shape. A backslash anywhere is not the shape.
private val HANDSHAKE_LINE =
    Regex("^\\s*\\{\\s*\"url\"\\s*:\\s*\"([^\"\\\\]*)\"\\s*,\\s*\"addr\"\\s*:\\s*\"([^\"\\\\]*)\"\\s*\\}\\s*$")
private val HANDSHAKE_URL = Regex("^http://127\\.0\\.0\\.1:(\\d{1,5})/\\?token=[A-Za-z0-9_-]+$")
private val HANDSHAKE_ADDR = Regex("^127\\.0\\.0\\.1:(\\d{1,5})$")

/**
 * The handshake in [line], or null when it is any other line. The gateway
 * may print a stray warning first, so a line that is not the handshake is
 * skipped, not an error. The URL must be the tokened loopback one on the
 * port [Handshake.addr] names: the WebView is locked to its origin, so
 * nothing else may become that origin.
 */
fun parseHandshake(line: String): Handshake? {
    val (url, addr) = HANDSHAKE_LINE.matchEntire(line)?.destructured ?: return null
    val urlPort = HANDSHAKE_URL.matchEntire(url)?.groupValues?.get(1) ?: return null
    val addrPort = HANDSHAKE_ADDR.matchEntire(addr)?.groupValues?.get(1) ?: return null
    if (urlPort != addrPort || urlPort.toInt() > 65535) return null
    return Handshake(url, addr)
}

/**
 * The resolv.conf the CLI's resolver reads. Stock Android has no
 * /etc/resolv.conf. An IPv6 server keeps its zone (fe80::1%wlan0), which the
 * CLI accepts.
 */
fun resolvConf(servers: List<String>): String =
    servers.joinToString("") { "nameserver $it\n" }

private const val APEX_CA_DIR = "/apex/com.android.conscrypt/cacerts"
private const val SYSTEM_CA_DIR = "/system/etc/security/cacerts"

/**
 * The directory of Android's system CA store. Android 14 serves it from the
 * updatable conscrypt APEX; before that it is the read-only system copy.
 */
fun caDirectory(apexExists: Boolean): String = if (apexExists) APEX_CA_DIR else SYSTEM_CA_DIR

// The exit status `aether gui --json` uses to say it replaced its own binary
// (localgw.ExitRelaunch).
private const val RELAUNCH_EXIT = 75
private const val MAX_RESPAWNS = 3
private const val HANDSHAKE_TIMEOUT_SECONDS = 15L
private const val STDERR_LINES = 40
private const val STABLE_RUN_MS = 30_000L

/**
 * One run of the bundled `aether gui --json` sidecar, which holds the device
 * key and tokens and serves the dashboard on loopback. It restarts the
 * process the way the desktop shell does, and is done for good after
 * [stop] or once it gives up. Callbacks arrive on the main thread.
 */
class Gateway(
    context: Context,
    private val port: Int,
    private val onReady: (url: String) -> Unit,
    private val onFailed: (message: String) -> Unit,
    private val onExited: () -> Unit,
) {
    private val context = context.applicationContext
    private val main = Handler(Looper.getMainLooper())
    private val stderrTail = ArrayDeque<String>()
    private val resolvLock = Any()

    @Volatile private var stopping = false
    @Volatile private var process: Process? = null
    private var supervisor: Thread? = null

    /** Whether the sidecar is running or waiting to be restarted. */
    val isActive: Boolean
        get() = supervisor?.isAlive == true

    fun start() {
        val thread = Thread(::supervise, "aether-gateway")
        supervisor = thread
        thread.start()
    }

    fun stop() {
        stopping = true
        supervisor?.interrupt()
        val doomed = process ?: return
        // Off the main thread: the wait is the point.
        Thread {
                doomed.destroy()
                if (!doomed.waitFor(5, TimeUnit.SECONDS)) doomed.destroyForcibly()
            }
            .start()
    }

    private fun supervise() {
        val bin = File(context.applicationInfo.nativeLibraryDir, "libaether.so")
        if (!bin.canExecute()) {
            fail("The aether CLI is not in this build: ${bin.absolutePath} is missing. " +
                "Build the app with `make android`.")
            return
        }
        val connectivity = context.getSystemService(ConnectivityManager::class.java)
        try {
            writeResolvConf(connectivity.getLinkProperties(connectivity.activeNetwork))
        } catch (e: IOException) {
            fail("Could not write the DNS servers for the CLI: ${e.message}")
            return
        }
        val callback =
            object : ConnectivityManager.NetworkCallback() {
                override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) {
                    try {
                        writeResolvConf(properties)
                    } catch (e: IOException) {
                        // Left running, the CLI would resolve against the
                        // last network's servers and fail every lookup.
                        val message = "Could not update the DNS servers for the CLI: ${e.message}"
                        main.post {
                            if (!stopping) {
                                stop()
                                onFailed(message)
                            }
                        }
                    }
                }
            }
        connectivity.registerDefaultNetworkCallback(callback)
        try {
            superviseProcess(bin)
        } finally {
            connectivity.unregisterNetworkCallback(callback)
        }
    }

    @Volatile private var timedOut = false

    private fun superviseProcess(bin: File) {
        var respawns = 0
        var listen = port
        while (!stopping) {
            var servedAt = 0L
            val code =
                try {
                    runOnce(bin, listen) { servedAt = SystemClock.elapsedRealtime() }
                } catch (_: InterruptedException) {
                    return
                } catch (e: IOException) {
                    fail("Could not start ${bin.absolutePath}: ${e.message}")
                    return
                }
            if (stopping) return
            main.post { if (!stopping) onExited() }
            // Another app may hold the port. An ephemeral one costs only the
            // dashboard's saved settings, until the next start.
            val served = servedAt != 0L
            if (!served) listen = 0
            // Only a run that stayed up resets the count: one that exits
            // right after its handshake would otherwise restart forever.
            if (served && SystemClock.elapsedRealtime() - servedAt >= STABLE_RUN_MS) respawns = 0
            try {
                if (code == RELAUNCH_EXIT) {
                    // The gateway replaced its binary. Desktop relaunches its
                    // shell; this one has no rebuild step, so a fresh start is
                    // the whole of it, and not a failure.
                    Thread.sleep(1000)
                    continue
                }
                respawns += 1
                if (respawns > MAX_RESPAWNS) {
                    val why =
                        if (timedOut) "printed no handshake within ${HANDSHAKE_TIMEOUT_SECONDS}s and was stopped"
                        else "exited with code $code"
                    fail("aether gui $why\n\n${stderrText()}")
                    return
                }
                Thread.sleep(1000L shl (respawns - 1))
            } catch (_: InterruptedException) {
                return
            }
        }
    }

    /** Runs the sidecar to its exit and returns the exit code. */
    private fun runOnce(bin: File, port: Int, onHandshake: () -> Unit): Int {
        val builder =
            ProcessBuilder(bin.absolutePath, "gui", "--json", "--port", port.toString())
        builder.environment().apply {
            put("HOME", context.filesDir.absolutePath)
            put("TMPDIR", context.cacheDir.absolutePath)
            put("AETHER_PACKAGED", "Android app")
            put("AETHER_RESOLV_CONF", resolvConfFile().absolutePath)
            // The x86_64 CLI is a linux/amd64 build (Go's android/amd64 needs
            // cgo), which knows only the CA paths of desktop Linux. Only the
            // system store: the app's network security config does not trust
            // user-installed CAs, so neither may the CLI.
            put("SSL_CERT_DIR", caDirectory(File(APEX_CA_DIR).isDirectory))
        }
        synchronized(stderrTail) { stderrTail.clear() }
        val child = builder.start()
        process = child
        if (stopping) child.destroy()
        child.outputStream.close()

        val stderr = Thread { drainStderr(child) }.apply { start() }
        val settled = CountDownLatch(1)
        Thread {
                val reader = child.inputStream.bufferedReader()
                var handshake: Handshake? = null
                // Reading on after the handshake keeps a full stdout pipe from
                // stalling the CLI.
                while (true) {
                    // A pipe closed under the read is the process ending.
                    val line = try { reader.readLine() } catch (_: IOException) { null } ?: break
                    if (handshake != null) continue
                    handshake = parseHandshake(line) ?: continue
                    onHandshake()
                    val url = handshake.url
                    main.post { if (!stopping) onReady(url) }
                    settled.countDown()
                }
                settled.countDown()
            }
            .start()
        // A gateway hung on startup neither talks nor dies, so the exit
        // path alone would never take over.
        timedOut = !settled.await(HANDSHAKE_TIMEOUT_SECONDS, TimeUnit.SECONDS)
        if (timedOut) child.destroyForcibly()
        val code = child.waitFor()
        stderr.join(1000)
        return code
    }

    private fun stderrText() = synchronized(stderrTail) { stderrTail.joinToString("\n") }

    private fun drainStderr(child: Process) {
        val reader = child.errorStream.bufferedReader()
        while (true) {
            val line = try { reader.readLine() } catch (_: IOException) { null } ?: return
            synchronized(stderrTail) {
                stderrTail.addLast(line)
                if (stderrTail.size > STDERR_LINES) stderrTail.removeFirst()
            }
        }
    }

    private fun fail(message: String) {
        main.post { if (!stopping) onFailed(message) }
    }

    private fun resolvConfFile() = File(context.filesDir, "resolv.conf")

    private fun writeResolvConf(properties: LinkProperties?) {
        val servers = properties?.dnsServers?.mapNotNull { it.hostAddress } ?: return
        synchronized(resolvLock) {
            // Renamed into place so the CLI, which reads the file at each
            // dial, never sees half of it.
            val temp = File(context.filesDir, "resolv.conf.tmp")
            temp.writeText(resolvConf(servers))
            if (!temp.renameTo(resolvConfFile())) {
                throw IOException("could not replace ${resolvConfFile()}")
            }
        }
    }
}
