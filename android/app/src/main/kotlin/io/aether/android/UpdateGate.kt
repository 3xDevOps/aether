package io.aether.android

import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.os.Build
import android.text.format.DateFormat
import android.os.Handler
import android.os.Looper
import android.view.View
import android.widget.Button
import android.widget.TextView
import android.widget.Toast
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.util.Date
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

private const val GATE_TIMEOUT_MS = 30_000

/**
 * The full-window panel that replaces the Edge dashboard while the server
 * refuses this app as too old. It cannot be dismissed: the way past it is
 * installing the update, which restarts the app and with it the gateway
 * whose answer clears the gate, or changing the connection. Every resume checks
 * again. [onChange] hears whether the gate is up.
 */
class UpdateGate(
    private val activity: Activity,
    private val panel: View,
    onConnection: () -> Unit,
    private val onChange: (shown: Boolean) -> Unit,
) {
    private val main = Handler(Looper.getMainLooper())
    private val executor: ExecutorService = Executors.newSingleThreadExecutor()
    private var destroyed = false
    private var checking = false
    private var gate: Gate? = null

    /** Bumped by [reset], so an answer from an earlier gateway is dropped. */
    private var generation = 0

    private val detail = panel.findViewById<TextView>(R.id.update_gate_detail)
    private val how = panel.findViewById<TextView>(R.id.update_gate_how)
    private val url = panel.findViewById<TextView>(R.id.update_gate_url)
    private val status = panel.findViewById<TextView>(R.id.update_gate_status)
    private val error = panel.findViewById<TextView>(R.id.update_gate_error)
    private val download = panel.findViewById<Button>(R.id.update_gate_download)

    val isShown: Boolean
        get() = gate != null

    private var facts: InstallFacts? = null
    private var gatewayUrl: String? = null

    init {
        download.setOnClickListener { openUpdate(activity, targets(), error) }
        panel.findViewById<Button>(R.id.update_gate_check).setOnClickListener {
            gatewayUrl?.let { check(it, refresh = true) }
        }
        panel.findViewById<Button>(R.id.update_gate_connection).setOnClickListener { onConnection() }
        panel.findViewById<Button>(R.id.update_gate_copy).setOnClickListener { copyDetails() }
    }

    /**
     * Asks the gateway at [handshakeUrl] whether the server still needs a
     * newer app. A check already running on the same gateway answers for
     * this one too: a refused page can answer 426 for every call it makes
     * at once.
     */
    fun check(handshakeUrl: String, refresh: Boolean = false) {
        if (destroyed) return
        if (handshakeUrl != gatewayUrl) {
            generation++
            checking = false
        }
        gatewayUrl = handshakeUrl
        val call = gatewayCall(handshakeUrl, "update.check") ?: return
        if (checking) return
        checking = true
        val asked = generation
        if (gate != null) status.setText(R.string.update_gate_checking)
        val context = activity.applicationContext
        executor.execute {
            val read = installFacts(context)
            val answer =
                try {
                    gateAnswer(post(call, if (refresh) "{\"refresh\":true}" else "{}"))
                } catch (e: IOException) {
                    Result.failure(e)
                }
            main.post {
                if (destroyed || asked != generation) return@post
                checking = false
                facts = read
                answer.fold(
                    onSuccess = {
                        show(it)
                        if (gate != null) {
                            val time = DateFormat.getTimeFormat(activity).format(Date())
                            status.text = activity.getString(R.string.update_gate_checked, time)
                        }
                    },
                    // An unanswered check changes nothing: the server still
                    // enforces, and a gate already up stays up.
                    onFailure = {
                        status.text = activity.getString(R.string.update_gate_check_failed, it.message ?: it.toString())
                    },
                )
            }
        }
    }

    fun reset() {
        generation++
        checking = false
        gatewayUrl = null
        show(null)
    }

    fun destroy() {
        destroyed = true
        executor.shutdownNow()
    }

    private fun show(next: Gate?) {
        val was = gate != null
        gate = next
        status.text = ""
        if (next == null) {
            panel.visibility = View.GONE
            if (was) onChange(false)
            return
        }
        val play = facts?.source == InstallSource.PLAY
        detail.text = activity.getString(R.string.update_gate_detail, next.clientVersion, next.serverVersion, next.minClientVersion)
        how.setText(if (play) R.string.update_gate_play else R.string.update_gate_release)
        download.setText(if (play) R.string.update_open_play else R.string.update_download)
        url.text = targets().joinToString("\n") { it.url }
        panel.visibility = View.VISIBLE
        if (!was) onChange(true)
    }

    private fun targets(): List<UpdateTarget> {
        val shown = gate ?: return emptyList()
        val source = facts?.source ?: return emptyList()
        return updateTargets(source, activity.packageName, shown.tag).orEmpty()
    }

    private fun copyDetails() {
        val shown = gate ?: return
        val clipboard = activity.getSystemService(ClipboardManager::class.java)
        clipboard.setPrimaryClip(ClipData.newPlainText("Aether update", gateDetails(shown, targets())))
        // Android 13 and newer confirm a copy themselves.
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            Toast.makeText(activity, R.string.update_gate_copied, Toast.LENGTH_SHORT).show()
        }
    }
}

private fun post(call: GatewayCall, body: String): String {
    val connection = URL(call.url).openConnection() as HttpURLConnection
    try {
        connection.requestMethod = "POST"
        connection.connectTimeout = GATE_TIMEOUT_MS
        connection.readTimeout = GATE_TIMEOUT_MS
        connection.doOutput = true
        connection.setRequestProperty("Authorization", "Bearer ${call.token}")
        connection.setRequestProperty("Content-Type", "application/json")
        connection.outputStream.use { it.write(body.toByteArray()) }
        val code = connection.responseCode
        if (code != HttpURLConnection.HTTP_OK) {
            val text = connection.errorStream?.bufferedReader()?.use { it.readText() }.orEmpty()
            throw IOException("update.check answered HTTP $code: ${text.trim()}")
        }
        return connection.inputStream.bufferedReader().use { it.readText() }
    } finally {
        connection.disconnect()
    }
}
