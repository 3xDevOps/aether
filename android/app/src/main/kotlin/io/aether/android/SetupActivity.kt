package io.aether.android

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.format.DateFormat
import android.view.KeyEvent
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import java.util.Date
import java.util.concurrent.Executors

/**
 * The first-run and settings screen: a choice between Aether Edge and
 * Tailscale, for Tailscale one field for the server's tailnet name, and
 * "Check for updates", which always answers.
 *
 * It stores no credential. Edge sign-in happens in the dashboard, and the
 * CLI sidecar keeps the resulting device key and tokens in app-private
 * storage (see [Gateway]).
 *
 * Reached on first launch, from the launcher's "Connection" shortcut, and
 * from the button a failed load shows.
 */
class SetupActivity : ComponentActivity() {
    private var checkDone: ((InstallFacts) -> Unit)? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.enableEdgeToEdge(window)
        setContentView(R.layout.activity_setup)

        // This screen is the shell's own UI, so unlike the WebView it keeps
        // itself out of the system bars and out from under the keyboard.
        val root = findViewById<View>(R.id.root)
        ViewCompat.setOnApplyWindowInsetsListener(root) { view, windowInsets ->
            val insets =
                windowInsets.getInsets(
                    WindowInsetsCompat.Type.systemBars() or
                        WindowInsetsCompat.Type.displayCutout() or
                        WindowInsetsCompat.Type.ime(),
                )
            view.setPadding(insets.left, insets.top, insets.right, insets.bottom)
            WindowInsetsCompat.CONSUMED
        }

        val choice = findViewById<View>(R.id.choice)
        val form = findViewById<View>(R.id.form)
        val address = findViewById<EditText>(R.id.address)
        val error = findViewById<TextView>(R.id.error)
        storedDashboardUrl()?.let { address.setText(it) }

        connectionMode()?.let { mode ->
            val current = findViewById<TextView>(R.id.choice_current)
            current.text =
                getString(
                    R.string.choice_current,
                    getString(if (mode == ConnectionMode.EDGE) R.string.choice_edge else R.string.choice_tailscale),
                )
            current.visibility = View.VISIBLE
        }

        findViewById<Button>(R.id.choose_edge).setOnClickListener {
            storeConnectionMode(ConnectionMode.EDGE)
            // MainActivity is singleTask: this reaches the running window
            // through onNewIntent, which starts the gateway.
            startActivity(Intent(this, MainActivity::class.java))
            finish()
        }
        findViewById<Button>(R.id.choose_tailscale).setOnClickListener {
            choice.visibility = View.GONE
            form.visibility = View.VISIBLE
            address.requestFocus()
        }

        fun submit() {
            val url =
                try {
                    dashboardUrl(address.text.toString())
                } catch (e: ServerAddressError) {
                    error.text = getString(e.reason, *e.detail)
                    error.visibility = View.VISIBLE
                    return
                }
            error.visibility = View.GONE
            storeDashboardUrl(url)
            // MainActivity is singleTask: this reaches the running window
            // through onNewIntent, which reloads it at the new address.
            startActivity(Intent(this, MainActivity::class.java))
            finish()
        }

        findViewById<Button>(R.id.open).setOnClickListener { submit() }
        setUpUpdateCheck()
        address.setOnEditorActionListener { _, _, event ->
            // A hardware Enter arrives twice, key-down then key-up.
            if (event == null || event.action == KeyEvent.ACTION_DOWN) submit()
            true
        }
    }

    override fun onDestroy() {
        if (ManualCheck.listener === checkDone) ManualCheck.listener = null
        super.onDestroy()
    }

    private fun setUpUpdateCheck() {
        val button = findViewById<Button>(R.id.check_updates)
        val status = findViewById<TextView>(R.id.update_status)
        val get = findViewById<Button>(R.id.update_get)
        val openError = findViewById<TextView>(R.id.update_open_error)
        var targets = emptyList<UpdateTarget>()
        get.setOnClickListener { openUpdate(this, targets, openError) }

        fun render(stored: StoredUpdate, facts: InstallFacts) {
            if (stored.checkedAt == 0L || (stored.line == null && stored.error == null)) return
            // A check from before this app was installed judged another version.
            if (stored.checkedAt < facts.updatedAt) return
            val time = DateFormat.getTimeFormat(this).format(Date(stored.checkedAt))
            val check = stored.line?.takeIf { stored.error == null }?.let(::parseUpdateCheck)
            val tag = check?.latest?.takeIf { check.updateAvailable && isReleaseTag(it) }
            status.text =
                when {
                    check == null -> getString(R.string.update_failed_at, time, stored.error ?: "")
                    tag != null -> getString(R.string.update_available, tag, check.version, time)
                    check.latest.isEmpty() -> getString(R.string.update_not_checked, check.version, time)
                    else -> getString(R.string.update_up_to_date, check.version, time)
                }
            status.setTextColor(getColor(if (check == null) R.color.error_text else R.color.text))
            status.visibility = View.VISIBLE
            targets = if (tag != null) updateTargets(facts.source, packageName, tag).orEmpty() else emptyList()
            get.setText(if (facts.source == InstallSource.PLAY) R.string.update_open_play else R.string.update_download)
            get.visibility = if (targets.isEmpty()) View.GONE else View.VISIBLE
        }

        fun showChecking() {
            button.isEnabled = false
            status.setText(R.string.update_checking)
            status.setTextColor(getColor(R.color.text))
            status.visibility = View.VISIBLE
            get.visibility = View.GONE
            openError.visibility = View.GONE
        }

        val done = { facts: InstallFacts ->
            button.isEnabled = true
            render(storedUpdate(), facts)
        }
        checkDone = done
        ManualCheck.listener = done
        if (ManualCheck.running) showChecking()

        Thread {
                val facts = installFacts(applicationContext)
                runOnUiThread {
                    // A check the member started since owns the status.
                    if (!isDestroyed && !ManualCheck.running) render(storedUpdate(), facts)
                }
            }
            .start()
        button.setOnClickListener {
            showChecking()
            ManualCheck.start(applicationContext)
        }
    }
}

/**
 * The check "Check for updates" started. It outlives the screen: a rotation
 * recreates SetupActivity mid-check, and the new one hears the answer.
 * Main thread only.
 */
private object ManualCheck {
    private val main = Handler(Looper.getMainLooper())
    private val executor = Executors.newSingleThreadExecutor()

    var running = false
        private set

    var listener: ((InstallFacts) -> Unit)? = null

    fun start(context: Context) {
        if (running) return
        running = true
        executor.execute {
            val facts = installFacts(context)
            val run = runCheck(context)
            main.post {
                recordCheck(context, run, facts.updatedAt)
                running = false
                listener?.invoke(facts)
            }
        }
    }
}
