package io.aether.android

import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.graphics.Bitmap
import android.net.Uri
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.graphics.Insets
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat

/**
 * The whole shell: one WebView on the dashboard the server hosts on the
 * tailnet.
 *
 * It holds no credential and no logic. Identity is this phone's tailnet
 * login, resolved per request by the server (docs/security.md), so the
 * WebView is a browser locked to one HTTPS origin and nothing more.
 */
class MainActivity : ComponentActivity() {
    private lateinit var web: WebView
    private lateinit var changeServer: Button
    private lateinit var pickFiles: ActivityResultLauncher<Intent>

    /** The dashboard the WebView is on, so a changed address reloads it. */
    private var loaded: String? = null
    private var fileChooser: ValueCallback<Array<Uri>>? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.enableEdgeToEdge(window)
        setContentView(R.layout.activity_main)
        web = findViewById(R.id.web)
        changeServer = findViewById(R.id.change_server)
        changeServer.setOnClickListener { openSetup() }

        pickFiles =
            registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
                val callback = fileChooser ?: return@registerForActivityResult
                fileChooser = null
                callback.onReceiveValue(
                    WebChromeClient.FileChooserParams.parseResult(result.resultCode, result.data),
                )
            }

        applyInsets()
        configureWebView()

        onBackPressedDispatcher.addCallback(
            this,
            object : OnBackPressedCallback(true) {
                override fun handleOnBackPressed() {
                    // The dashboard pushes its own history, so back walks it
                    // and then leaves the app running: finishing would drop
                    // the terminal's scrollback on the way to the home screen.
                    if (web.canGoBack()) web.goBack() else moveTaskToBack(true)
                }
            },
        )

        open(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        open(intent)
    }

    /**
     * The dashboard paints under the status and navigation bars and pads
     * itself back out with `env(safe-area-inset-*)`, so on a WebView that
     * reports safe areas the insets are handed through untouched. On an older
     * WebView they never reach the page, so the shell pads for them itself
     * and zeroes what it handled, and the page's `env()` reads 0 either way.
     *
     * The keyboard is padding in both cases. WebView's own IME handling
     * shrinks the visual viewport and leaves the page's layout alone;
     * shortening the WebView shrinks the layout viewport, which is what the
     * dashboard's `interactive-widget=resizes-content` and its `dvh` sizing
     * are written against. The IME inset is zeroed on the way down so the
     * WebView does not shrink a second time, and never consumed: consuming
     * would strand the padding when the keyboard closes.
     */
    private fun applyInsets() {
        val root = findViewById<View>(R.id.root)
        val margin = resources.getDimensionPixelSize(R.dimen.edge_margin)
        val pageHandlesSafeAreas =
            forwardsSafeAreaInsets(WebView.getCurrentWebViewPackage()?.versionName)
        val imeType = WindowInsetsCompat.Type.ime()
        val barType = WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout()
        ViewCompat.setOnApplyWindowInsetsListener(root) { view, windowInsets ->
            val ime = windowInsets.getInsets(imeType).bottom
            val bars = windowInsets.getInsets(barType)
            // The container is padded, not the WebView: a WebView scrolls its
            // page under its own padding instead of shrinking the viewport.
            if (pageHandlesSafeAreas) {
                view.setPadding(0, 0, 0, ime)
            } else {
                view.setPadding(bars.left, bars.top, bars.right, maxOf(bars.bottom, ime))
            }
            // The button is a sibling of the WebView inside that container, so
            // it needs the navigation bar's height only when the container
            // itself was not padded for it.
            val params = changeServer.layoutParams as ViewGroup.MarginLayoutParams
            params.bottomMargin = margin + if (pageHandlesSafeAreas) bars.bottom else 0
            changeServer.requestLayout()
            val forwarded = WindowInsetsCompat.Builder(windowInsets).setInsets(imeType, Insets.NONE)
            if (!pageHandlesSafeAreas) forwarded.setInsets(barType, Insets.NONE)
            forwarded.build()
        }
    }

    private fun configureWebView() {
        web.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            // The page sets its own viewport (width=device-width,
            // viewport-fit=cover); without this the WebView imposes a 980px
            // desktop viewport and ignores it.
            useWideViewPort = true
            // Belt and braces over the network security config: no
            // subresource may downgrade the page to cleartext.
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            allowFileAccess = false
            allowContentAccess = false
        }
        // Lets `chrome://inspect` attach to a debuggable build; a release
        // build stays closed. See docs/dashboard-frontend.md.
        WebView.setWebContentsDebuggingEnabled(
            applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0,
        )

        web.webViewClient =
            object : WebViewClient() {
                override fun shouldOverrideUrlLoading(
                    view: WebView,
                    request: WebResourceRequest,
                ): Boolean {
                    val target = request.url
                    // Multiple windows are unsupported, so `target=_blank`
                    // and `window.open` arrive here as top-level navigations
                    // too.
                    val decision =
                        navigationFor(
                            loaded,
                            target.toString(),
                            target.scheme?.lowercase(),
                            request.isForMainFrame,
                        )
                    if (decision == Navigation.EXTERNAL) openExternally(target)
                    return decision != Navigation.LOAD
                }

                override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
                    changeServer.visibility = View.GONE
                }

                override fun onReceivedError(
                    view: WebView,
                    request: WebResourceRequest,
                    error: WebResourceError,
                ) {
                    // The WebView's own error page stays on screen - it names
                    // what actually failed. This only adds the way back to
                    // the address that failed. An HTTP status is not this
                    // case: the server answered, so the address is right.
                    if (request.isForMainFrame) changeServer.visibility = View.VISIBLE
                }
            }

        web.webChromeClient =
            object : WebChromeClient() {
                override fun onShowFileChooser(
                    view: WebView,
                    callback: ValueCallback<Array<Uri>>,
                    params: FileChooserParams,
                ): Boolean {
                    // The callback has to fire exactly once, cancellation
                    // included, or the page's file input stays wedged.
                    fileChooser?.onReceiveValue(null)
                    fileChooser = callback
                    try {
                        pickFiles.launch(params.createIntent())
                    } catch (_: ActivityNotFoundException) {
                        fileChooser = null
                        callback.onReceiveValue(null)
                    }
                    return true
                }
            }
    }

    /**
     * Point the WebView at the stored dashboard, or at the run an
     * `aether://run/<id>` link names.
     */
    private fun open(intent: Intent?) {
        val base = storedDashboardUrl()
        if (base == null) {
            // A deep link that arrives before a server address is set is
            // dropped here: setup is a separate Activity, and carrying the
            // link through it would have to survive the member abandoning
            // that screen. Tapping the link again after setup works.
            openSetup()
            finish()
            return
        }
        val link = intent?.dataString?.let { deepLinkUrl(base, it) }
        if (link != null) {
            loaded = base
            web.loadUrl(link)
            return
        }
        if (base != loaded) {
            loaded = base
            web.loadUrl(base)
        }
    }

    private fun openSetup() {
        startActivity(Intent(this, SetupActivity::class.java))
    }

    private fun openExternally(target: Uri) {
        val intent =
            Intent(Intent.ACTION_VIEW, target)
                .addCategory(Intent.CATEGORY_BROWSABLE)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        try {
            startActivity(intent)
        } catch (_: ActivityNotFoundException) {
            Toast.makeText(this, getString(R.string.no_app_for_link, target), Toast.LENGTH_LONG)
                .show()
        }
    }
}
