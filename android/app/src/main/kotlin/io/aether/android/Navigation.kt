package io.aether.android

import java.net.URI
import java.net.URISyntaxException

/** What the shell does with a navigation the page started. */
enum class Navigation {
    /** Let the WebView load it: the dashboard, and the page's own blob: and data: URLs. */
    LOAD,

    /** Hand it to the system browser. */
    EXTERNAL,

    /** Neither. Consumed, so the WebView stays where it is. */
    DROP,
}

// Schemes the shell hands to the system.
private val EXTERNAL_SCHEMES = setOf("http", "https", "mailto", "tel")

// Schemes the page uses on itself, which nothing but the WebView can resolve.
private val INTERNAL_SCHEMES = setOf("blob", "data", "about")

/**
 * Where a navigation off the page should go.
 *
 * [base] is the dashboard the WebView is on, [scheme] the request's scheme
 * lowercased, and [mainFrame] its `isForMainFrame`.
 *
 * The dashboard's own origin stays in the WebView and an ordinary link
 * elsewhere goes to the browser. Anything else is dropped rather than loaded
 * or launched: a page must not be able to start another app through an
 * `intent://` URL carrying extras of its own choosing, and letting the
 * WebView try instead would replace the dashboard with an
 * `ERR_UNKNOWN_URL_SCHEME` page that reads like the server address is wrong.
 *
 * A subframe never leaves the shell. The dashboard has no iframes, so an
 * off-origin subframe navigation is not a link anyone clicked.
 */
fun navigationFor(base: String?, url: String, scheme: String?, mainFrame: Boolean): Navigation {
    if (scheme in INTERNAL_SCHEMES) return Navigation.LOAD
    if (scheme !in EXTERNAL_SCHEMES) return Navigation.DROP
    if (base != null && isDashboardUrl(base, url)) return Navigation.LOAD
    if (!mainFrame) return Navigation.DROP
    return Navigation.EXTERNAL
}

/**
 * Whether a request the WebView is about to send must be refused outright.
 *
 * WebView does not call `shouldOverrideUrlLoading` for a POST, so a form on
 * the page reaches any origin without passing that gate.
 * `shouldInterceptRequest` is called for one, and before anything goes on the
 * wire, so a main-frame request off the dashboard's origin is refused here
 * rather than after its response has committed.
 *
 * Only main-frame http and https: a subresource is the dashboard loading its
 * own fonts and images, and the page's own `blob:` and `data:` URLs carry no
 * request at all.
 */
fun refusesRequest(base: String?, url: String, scheme: String?, mainFrame: Boolean): Boolean {
    if (!mainFrame) return false
    if (scheme != "http" && scheme != "https") return false
    return base == null || !isDashboardUrl(base, url)
}

/**
 * Whether a main-frame load that has already started is still on the
 * dashboard.
 *
 * [refusesRequest] empties the response of an off-origin POST, but the
 * navigation still commits, on a blank document. This runs from
 * `onPageStarted`, which is handed the URL as a string and nothing else, and
 * is what puts the dashboard back. An unparseable URL has no origin to match,
 * so it is not the dashboard - the same reading [isDashboardUrl] takes.
 */
fun startedOnDashboard(base: String?, url: String): Boolean {
    val scheme =
        try {
            URI(url).scheme?.lowercase()
        } catch (_: URISyntaxException) {
            null
        }
    return navigationFor(base, url, scheme, mainFrame = true) == Navigation.LOAD
}
