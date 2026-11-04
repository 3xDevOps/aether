package io.aether.android

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.webkit.WebView
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.content.ContextCompat
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebMessageCompat
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import java.net.URI
import org.json.JSONException
import org.json.JSONObject
import org.unifiedpush.android.connector.UnifiedPush

private const val BRIDGE = "AetherAndroid"

/** The `scheme://host[:port]` of [url], as a web message origin rule takes it. */
fun originOf(url: String): String {
    val uri = URI(url)
    return "${uri.scheme}://${uri.rawAuthority}"
}

/**
 * The dashboard's push switch, for this phone: the page posts `state`,
 * `enable` or `disable` to `window.AetherAndroid` and is sent this phone's
 * [PushState] back, and again whenever it changes.
 *
 * The object exists only in the dashboard's own main frame: WebView injects
 * it into no other origin and the listener ignores subframes. It carries
 * nothing a page could not already do as the member - read whether this
 * phone is subscribed, and turn that on or off.
 */
class PushBridge(private val activity: ComponentActivity) {
    private var reply: JavaScriptReplyProxy? = null
    private var installed: WebView? = null

    private val permission =
        activity.registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) PushRegistration.enable(activity) else send(PushState(PushKind.BLOCKED))
        }

    init {
        PushRegistration.onChange = { send(PushRegistration.state(activity)) }
    }

    /**
     * Offers the bridge to [web] while it shows the dashboard at [dashboard].
     * WebView applies it from the next page load, so call it before loading.
     */
    fun install(web: WebView, dashboard: String) {
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) return
        if (installed === web) WebViewCompat.removeWebMessageListener(web, BRIDGE)
        reply = null
        installed = web
        WebViewCompat.addWebMessageListener(web, BRIDGE, setOf(originOf(dashboard))) { _, message, _, mainFrame, proxy ->
            if (mainFrame) handle(message, proxy)
        }
    }

    /** Settles a state that changed while the app was away, such as a distributor installed meanwhile. */
    fun resume() {
        PushRegistration.sync(activity)
        send(PushRegistration.state(activity))
    }

    fun destroy() {
        PushRegistration.onChange = null
    }

    private fun handle(message: WebMessageCompat, proxy: JavaScriptReplyProxy) {
        reply = proxy
        val op =
            try {
                JSONObject(message.data ?: return).optString("op")
            } catch (_: JSONException) {
                return
            }
        when (op) {
            "enable" -> enable()
            "disable" -> PushRegistration.disable(activity)
            else -> send(PushRegistration.state(activity))
        }
    }

    /** No distributor, no permission prompt: the switch names the fix first. */
    private fun enable() {
        if (UnifiedPush.getDistributors(activity).isEmpty()) {
            send(PushState(PushKind.NO_DISTRIBUTOR, error = "Still no distributor. Install ntfy, then tap again."))
            return
        }
        val needsPermission =
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                ContextCompat.checkSelfPermission(activity, Manifest.permission.POST_NOTIFICATIONS) !=
                PackageManager.PERMISSION_GRANTED
        if (needsPermission) permission.launch(Manifest.permission.POST_NOTIFICATIONS) else PushRegistration.enable(activity)
    }

    private fun send(state: PushState) {
        val json = JSONObject().put("kind", state.kind.wire)
        state.endpoint?.let { json.put("endpoint", it) }
        state.error?.let { json.put("error", it) }
        val page = reply ?: return
        page.postMessage(json.toString())
        // Shown once: a later visit to Settings starts clean.
        if (state.error != null) PushRegistration.clearError(activity)
    }
}
