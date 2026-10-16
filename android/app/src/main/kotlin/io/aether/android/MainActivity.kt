package io.aether.android

import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.graphics.Bitmap
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Message
import android.util.Log
import android.view.View
import android.view.ViewGroup
import android.webkit.ConsoleMessage
import android.webkit.RenderProcessGoneDetail
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.ServiceWorkerController
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.webkit.WebViewRenderProcess
import android.webkit.WebViewRenderProcessClient
import android.widget.Button
import android.widget.TextView
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.graphics.Insets
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.lifecycle.Lifecycle
import java.io.ByteArrayInputStream
import java.util.concurrent.Executors

private const val TAG = "aether"

// Not the main thread: a WebView waiting on a hung renderer can be blocking
// it. One for the process, never shut down, since WebView may deliver to it
// after any one window is gone.
private val rendererWatch = Executors.newSingleThreadExecutor()

/**
 * The whole shell: one WebView on a dashboard.
 *
 * In Tailscale mode that is the one the server hosts on the tailnet. The
 * shell holds no credential and no logic there: identity is this phone's
 * tailnet login, resolved per request by the server (docs/security.md), so
 * the WebView is a browser locked to one HTTPS origin and nothing more.
 *
 * In Edge mode it is the one the bundled CLI serves on loopback, which this
 * window starts and owns as a [Gateway]. The CLI sidecar holds the device
 * key and tokens in app-private storage; the WebView itself still holds
 * none, and is locked to the gateway's one `http://127.0.0.1:<port>` origin.
 */
class MainActivity : ComponentActivity() {
    private lateinit var web: WebView
    private lateinit var connection: Button
    private lateinit var gatewayError: View
    private lateinit var pickFiles: ActivityResultLauncher<Intent>

    /**
     * The dashboard the WebView is on, so a changed address reloads it.
     * Volatile because `shouldInterceptRequest` reads it off the WebView's
     * own thread, and a stale null there would refuse the dashboard itself.
     */
    @Volatile
    private var loaded: String? = null

    private var mode = ConnectionMode.TAILSCALE
    private var gateway: Gateway? = null
    private var updates: UpdateNotice? = null
    private var gate: UpdateGate? = null

    /** The running gateway's address, once it has printed it. */
    private var gatewayUrl: String? = null

    /** A deep link's suffix held until the gateway has an address to append it to. */
    private var pendingSuffix = ""
    private var fileChooser: ValueCallback<Array<Uri>>? = null

    /**
     * The page a reclaimed renderer had open, empty when it had none, until
     * the dashboard is reloaded at it.
     */
    private var reclaimedPage: String? = null

    /** Set while the failure panel shows a lost renderer rather than the gateway. */
    private var rendererLost = false

    /**
     * Set when the shell ends a renderer that stopped responding. Written on
     * [rendererWatch], read where the renderer's loss is handled.
     */
    @Volatile
    private var terminatedRenderer = false

    /**
     * Whether the platform backgrounds this window itself when back is
     * pressed with no history left.
     *
     * It does that only from API 31, and only for a task whose root was
     * started from the launcher: `shouldMoveTaskToBack` wants both a home
     * launch source and a main intent. The session SetupActivity hands over
     * has neither, and there the platform finishes the window instead,
     * taking the WebView and the terminal's scrollback with it. Decided once
     * from the intent that created this window, because that is what the
     * platform reads.
     *
     * The test below is `ActivityRecord.isMainIntent`, clause for clause: a
     * looser one would stand the callback down for an intent the platform
     * then refuses to background.
     */
    private var platformBackgroundsUs = false

    /**
     * Set when a refused off-origin load has to be undone, so the recovery
     * load can drop the blank entry the refusal committed. Without that,
     * back walks into it, the gate refuses it again, and the member can
     * never leave.
     */
    private var clearHistoryOnLoad = false

    /**
     * Back walks the dashboard's own history and then leaves the app running:
     * finishing would drop the terminal's scrollback on the way to the home
     * screen. Where the platform does that itself the callback stands down at
     * the root, so the predictive back-to-home animation runs.
     */
    private val back =
        object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (gate?.isShown != true && web.canGoBack()) web.goBack() else moveTaskToBack(true)
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // Before setContentView, which instantiates the WebView: a fresh
        // install has nothing to open, and inflating the WebView first paints
        // an empty window on the way to the setup form. A deep link that
        // arrives before an address is set is dropped here - setup is a
        // separate Activity, and carrying the link through it would have to
        // survive the member abandoning that screen. Tapping the link again
        // after setup works.
        mode =
            connectionMode()
                ?: run {
                    openSetup()
                    finish()
                    return
                }
        WindowCompat.enableEdgeToEdge(window)
        setContentView(R.layout.activity_main)
        web = findViewById(R.id.web)
        connection = findViewById(R.id.connection)
        connection.setOnClickListener { openSetup() }
        gatewayError = findViewById(R.id.gateway_error)
        findViewById<Button>(R.id.gateway_retry).setOnClickListener {
            if (rendererLost) connect(null) else startGateway()
        }
        findViewById<Button>(R.id.gateway_connection).setOnClickListener { openSetup() }
        updates = UpdateNotice(this, findViewById(R.id.update_notice))
        gate =
            UpdateGate(this, findViewById(R.id.update_gate), onSwitchServer = ::openSetup) { shown ->
                web.visibility = if (shown) View.INVISIBLE else View.VISIBLE
                syncBackCallback()
                // The dashboard under the gate may be showing the refusal.
                if (!shown) loaded?.let(web::loadUrl)
            }

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

        platformBackgroundsUs =
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.S &&
                intent?.action == Intent.ACTION_MAIN &&
                intent.categories?.size == 1 &&
                intent.hasCategory(Intent.CATEGORY_LAUNCHER) &&
                intent.data == null &&
                intent.type == null
        back.isEnabled = !platformBackgroundsUs
        onBackPressedDispatcher.addCallback(this, back)

        // A task Android recreates after killing the process is handed its
        // root intent again, so a shell first launched from an
        // aether://run link would reopen that run on every return. The link
        // is one-shot here, as it is in the dashboard.
        connect(if (savedInstanceState == null) intent else null)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        // SetupActivity stores the mode and address before it starts this
        // window, so a change is already here to apply.
        val stored = connectionMode() ?: return
        if (stored != mode) {
            mode = stored
            applyModeSettings()
            gatewayUrl = null
            loaded = null
            web.stopLoading()
            stopGateway()
            gate?.reset()
        }
        connect(intent)
    }

    override fun onStart() {
        super.onStart()
        val page = reclaimedPage ?: return
        reclaimedPage = null
        loaded?.let { web.loadUrl(recoveryUrl(it, page)) }
    }

    override fun onResume() {
        super.onResume()
        updates?.resume()
        if (mode == ConnectionMode.EDGE && gateway?.isActive != true &&
            gatewayError.visibility != View.VISIBLE
        ) {
            startGateway()
        }
        gatewayUrl?.let { gate?.check(it) }
    }

    override fun onDestroy() {
        updates?.destroy()
        gate?.destroy()
        stopGateway()
        super.onDestroy()
    }

    private fun connect(intent: Intent?) {
        gatewayError.visibility = View.GONE
        // The WebView a renderer crash left behind is empty, so the
        // dashboard loads again even at an unchanged address.
        val reload = rendererLost
        rendererLost = false
        when (mode) {
            ConnectionMode.TAILSCALE -> {
                stopGateway()
                if (reload) loaded = null
                open(storedDashboardUrl() ?: return, intent)
            }
            ConnectionMode.EDGE -> {
                if (reload) loaded?.let(web::loadUrl)
                openEdge(intent)
            }
        }
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
        val notice = findViewById<View>(R.id.update_notice)
        val noticeMargin = resources.getDimensionPixelSize(R.dimen.update_notice_margin)
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
            val params = connection.layoutParams as ViewGroup.MarginLayoutParams
            params.bottomMargin = margin + if (pageHandlesSafeAreas) bars.bottom else 0
            connection.requestLayout()
            val gatePanel = findViewById<View>(R.id.update_gate)
            gatePanel.setPadding(
                margin,
                margin + if (pageHandlesSafeAreas) bars.top else 0,
                margin,
                margin + if (pageHandlesSafeAreas) bars.bottom else 0,
            )
            val noticeParams = notice.layoutParams as ViewGroup.MarginLayoutParams
            noticeParams.topMargin = noticeMargin + if (pageHandlesSafeAreas) bars.top else 0
            if (pageHandlesSafeAreas) {
                val rtl = notice.layoutDirection == View.LAYOUT_DIRECTION_RTL
                noticeParams.marginStart = noticeMargin + if (rtl) bars.right else bars.left
                noticeParams.marginEnd = noticeMargin + if (rtl) bars.left else bars.right
            }
            notice.requestLayout()
            val panel = gatewayError
            panel.setPadding(
                margin,
                margin + if (pageHandlesSafeAreas) bars.top else 0,
                margin,
                margin + if (pageHandlesSafeAreas) bars.bottom else 0,
            )
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
        applyModeSettings()
        val debuggable = applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0
        // Lets `chrome://inspect` attach to a debuggable build; a release
        // build stays closed. See docs/dashboard-frontend.md.
        WebView.setWebContentsDebuggingEnabled(debuggable)

        // WebView's main thread waits on its renderer without a limit, so a
        // renderer that is alive but stuck freezes the app into an ANR.
        // Ending it hands over to onRenderProcessGone, which shows it.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            web.setWebViewRenderProcessClient(
                rendererWatch,
                object : WebViewRenderProcessClient() {
                    private var reports = 0

                    override fun onRenderProcessUnresponsive(view: WebView, renderer: WebViewRenderProcess?) {
                        reports++
                        Log.w(TAG, "dashboard renderer not responding ($reports in a row)")
                        if (!endsUnresponsiveRenderer(reports)) return
                        // Set first: onRenderProcessGone can run before
                        // terminate returns.
                        terminatedRenderer = true
                        if (renderer?.terminate() == true) {
                            Log.w(TAG, "ended the dashboard renderer")
                        } else {
                            terminatedRenderer = false
                        }
                    }

                    override fun onRenderProcessResponsive(view: WebView, renderer: WebViewRenderProcess?) {
                        reports = 0
                    }
                },
            )
        }

        web.webViewClient =
            object : WebViewClient() {
                override fun shouldOverrideUrlLoading(
                    view: WebView,
                    request: WebResourceRequest,
                ): Boolean {
                    val target = request.url
                    // In Tailscale mode multiple windows are unsupported, so
                    // `target=_blank` and `window.open` arrive here as
                    // top-level navigations too.
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

                override fun shouldInterceptRequest(
                    view: WebView,
                    request: WebResourceRequest,
                ): WebResourceResponse? {
                    val target = request.url
                    if (
                        !refusesRequest(
                            loaded,
                            target.toString(),
                            target.scheme?.lowercase(),
                            request.isForMainFrame,
                        )
                    ) {
                        return null
                    }
                    // Runs on the WebView's own thread, before the request is
                    // sent: nothing of the member's reaches the other origin.
                    return WebResourceResponse("text/plain", "utf-8", ByteArrayInputStream(ByteArray(0)))
                }

                override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
                    connection.visibility = View.GONE
                    if (startedOnDashboard(loaded, url)) return
                    // The refused request still commits, on the blank
                    // document it was answered with, so the dashboard goes
                    // back on screen. Not handed to the browser: nothing that
                    // arrives here was a link anyone tapped, or the gate in
                    // shouldOverrideUrlLoading would have taken it. Loading
                    // from inside this callback waits for the next loop turn.
                    view.stopLoading()
                    loaded?.let { dashboard ->
                        clearHistoryOnLoad = true
                        view.post { view.loadUrl(dashboard) }
                    }
                }

                override fun onPageFinished(view: WebView, url: String) {
                    // A refusal on the dashboard's WebSocket never reaches
                    // onReceivedHttpError, so every gateway page asks too.
                    gatewayUrl?.let { if (url.startsWith(it.substringBefore("?"))) gate?.check(it) }
                    if (!clearHistoryOnLoad) return
                    clearHistoryOnLoad = false
                    view.clearHistory()
                    syncBackCallback()
                }

                override fun doUpdateVisitedHistory(
                    view: WebView,
                    url: String,
                    isReload: Boolean,
                ) {
                    syncBackCallback()
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
                    if (request.isForMainFrame) connection.visibility = View.VISIBLE
                }

                // webgate answers -32005 with HTTP 426 on any call from a
                // client the server refuses as too old, the page's included.
                override fun onReceivedHttpError(
                    view: WebView,
                    request: WebResourceRequest,
                    errorResponse: WebResourceResponse,
                ) {
                    // The first refusal asks past the gateway's release cache,
                    // which can predate the release the server now needs.
                    if (errorResponse.statusCode == 426) {
                        gatewayUrl?.let { gate?.check(it, refresh = gate?.isShown != true) }
                    }
                }

                // Unhandled, WebView kills the app along with its renderer.
                // Android reclaims the renderer of an app in the background,
                // as while the member signs in to Edge in the browser.
                override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                    // The page's file input dies with it.
                    fileChooser?.onReceiveValue(null)
                    fileChooser = null
                    val parent = view.parent as ViewGroup
                    val index = parent.indexOfChild(view)
                    val params = view.layoutParams
                    val page = view.url
                    parent.removeView(view)
                    view.destroy()
                    web =
                        WebView(this@MainActivity).apply {
                            id = R.id.web
                            if (gate?.isShown == true) visibility = View.INVISIBLE
                        }
                    parent.addView(web, index, params)
                    // The insets pass ran before this WebView existed.
                    ViewCompat.requestApplyInsets(parent)
                    configureWebView()
                    syncBackCallback()
                    val loss =
                        rendererLoss(
                            detail.didCrash(),
                            terminatedRenderer,
                            lifecycle.currentState.isAtLeast(Lifecycle.State.STARTED),
                        )
                    terminatedRenderer = false
                    Log.w(TAG, "dashboard renderer gone: $loss")
                    val message =
                        when (loss) {
                            RendererLoss.RELOAD -> {
                                reclaimedPage = page ?: ""
                                return true
                            }
                            RendererLoss.KILLED -> R.string.renderer_killed
                            RendererLoss.CRASHED -> R.string.renderer_crashed
                            RendererLoss.UNRESPONSIVE -> R.string.renderer_unresponsive
                        }
                    // The gateway's failure says more than this one: with no
                    // gateway there was no dashboard to lose.
                    if (gatewayError.visibility == View.VISIBLE && !rendererLost) return true
                    rendererLost = true
                    showFailure(getString(message), "")
                    return true
                }
            }

        web.webChromeClient =
            object : WebChromeClient() {
                // Returning true says the message is handled, which keeps the
                // page's console out of logcat. A debug build keeps it there,
                // where `adb logcat` is the point.
                override fun onConsoleMessage(message: ConsoleMessage): Boolean = !debuggable

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

                override fun onCreateWindow(
                    view: WebView,
                    isDialog: Boolean,
                    isUserGesture: Boolean,
                    resultMsg: Message,
                ): Boolean {
                    val transport = resultMsg.obj as? WebView.WebViewTransport ?: return false
                    transport.webView = popupWebView()
                    resultMsg.sendToTarget()
                    return true
                }

                override fun onCloseWindow(window: WebView) {
                    window.destroy()
                }
            }
    }

    /**
     * Edge mode only; Tailscale mode keeps the WebView's defaults.
     *
     * The gateway's port is fixed, so while the CLI is down another app could
     * bind it and serve the open page a cached chunk or new script that reads
     * its tokens. Nothing from that origin is cached, no service worker may
     * keep a copy, and the page is blanked whenever the CLI exits.
     */
    private fun applyModeSettings() {
        val edge = mode == ConnectionMode.EDGE
        web.settings.cacheMode = if (edge) WebSettings.LOAD_NO_CACHE else WebSettings.LOAD_DEFAULT
        web.settings.setSupportMultipleWindows(edge)
        ServiceWorkerController.getInstance().serviceWorkerWebSettings.apply {
            blockNetworkLoads = edge
            cacheMode = if (edge) WebSettings.LOAD_NO_CACHE else WebSettings.LOAD_DEFAULT
            allowContentAccess = !edge
            allowFileAccess = !edge
        }
    }

    /**
     * The window a `window.open` gets: never attached, never rendered, with
     * script off. It only learns the address the page is sending it to, which
     * goes to the system browser, and is destroyed once it has.
     */
    private fun popupWebView(): WebView {
        val popup = WebView(this)
        popup.settings.javaScriptEnabled = false
        popup.settings.blockNetworkLoads = true
        var done = false
        fun route(url: String, scheme: String?): Boolean {
            if (done) return true
            val decision = popupNavigation(url, scheme)
            if (decision == Navigation.LOAD) return false
            if (decision == Navigation.EXTERNAL) openExternally(Uri.parse(url))
            done = true
            // Through the attached WebView: a detached view holds what it is
            // posted until it is attached, which this one never is.
            web.post { popup.destroy() }
            return true
        }
        popup.webViewClient =
            object : WebViewClient() {
                override fun shouldOverrideUrlLoading(
                    view: WebView,
                    request: WebResourceRequest,
                ): Boolean = route(request.url.toString(), request.url.scheme?.lowercase())

                // A first load can skip shouldOverrideUrlLoading.
                override fun onPageStarted(view: WebView, url: String, favicon: Bitmap?) {
                    if (done) return
                    val scheme = Uri.parse(url).scheme?.lowercase()
                    if (popupNavigation(url, scheme) != Navigation.LOAD) {
                        view.stopLoading()
                        route(url, scheme)
                    }
                }

                // The popup shares the dashboard's renderer, and WebView kills
                // the app unless every WebView on a lost renderer handles it.
                override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                    done = true
                    view.destroy()
                    return true
                }
            }
        return popup
    }

    /**
     * Point the WebView at the dashboard at [base], or at the run an
     * `aether://run/<id>` link names.
     */
    private fun open(base: String, intent: Intent?) {
        // From here the new address is what every gate judges against, so
        // whatever the old one still has in flight stops before its commit
        // can be read as an escape off the dashboard.
        if (base != loaded) web.stopLoading()
        val raw = intent?.dataString
        if (raw != null && isEdgeOnlyLink(raw)) {
            Toast.makeText(this, R.string.join_needs_edge, Toast.LENGTH_LONG).show()
        }
        val link = raw?.let { deepLinkUrl(base, it) }
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

    /**
     * Shows the Edge dashboard: now if the gateway has an address, else when
     * it prints one. A link that names something to open is held until then.
     */
    private fun openEdge(intent: Intent?) {
        val suffix = intent?.dataString?.let(::edgeDeepLinkSuffix)
        val url = gatewayUrl
        when {
            gateway?.isActive != true -> {
                pendingSuffix = suffix ?: ""
                startGateway()
            }
            url == null -> if (suffix != null) pendingSuffix = suffix
            suffix != null -> web.loadUrl(url + suffix)
        }
    }

    private fun startGateway() {
        stopGateway()
        gatewayUrl = null
        gatewayError.visibility = View.GONE
        gateway =
            Gateway(
                this,
                gatewayPort(),
                onReady = { url ->
                    gatewayUrl = url
                    // A new address is a new origin: what the old one has in
                    // flight stops before it can be read as an escape.
                    web.stopLoading()
                    web.clearCache(true)
                    // The previous gateway's pages and the blank page its
                    // exit left are another origin; back must not reach them.
                    clearHistoryOnLoad = true
                    loaded = url
                    web.loadUrl(url + pendingSuffix)
                    pendingSuffix = ""
                    gate?.check(url)
                },
                onExited = {
                    gatewayUrl = null
                    loaded = null
                    web.stopLoading()
                    web.loadUrl("about:blank")
                },
                onFailed = { message ->
                    gatewayUrl = null
                    loaded = null
                    rendererLost = false
                    showFailure(getString(R.string.gateway_stopped), message)
                },
            )
        gateway?.start()
    }

    private fun showFailure(title: String, message: String) {
        findViewById<TextView>(R.id.gateway_error_title).text = title
        findViewById<TextView>(R.id.gateway_error_text).text = message
        gatewayError.visibility = View.VISIBLE
    }

    private fun stopGateway() {
        gateway?.stop()
        gateway = null
    }

    /**
     * Keeps the callback's claim on the gesture matched to the history the
     * WebView actually has, wherever that history changes.
     */
    private fun syncBackCallback() {
        if (platformBackgroundsUs) back.isEnabled = gate?.isShown != true && web.canGoBack()
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
