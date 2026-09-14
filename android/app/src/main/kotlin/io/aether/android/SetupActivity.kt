package io.aether.android

import android.content.Intent
import android.os.Bundle
import android.view.KeyEvent
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat

/**
 * The first-run and settings screen: one field for the server's tailnet name.
 *
 * Reached on first launch, from the launcher's "Server address" shortcut, and
 * from the button a failed load shows.
 */
class SetupActivity : ComponentActivity() {
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

        val address = findViewById<EditText>(R.id.address)
        val error = findViewById<TextView>(R.id.error)
        storedDashboardUrl()?.let { address.setText(it) }

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
        address.setOnEditorActionListener { _, _, event ->
            // A hardware Enter arrives twice, key-down then key-up.
            if (event == null || event.action == KeyEvent.ACTION_DOWN) submit()
            true
        }
    }
}
