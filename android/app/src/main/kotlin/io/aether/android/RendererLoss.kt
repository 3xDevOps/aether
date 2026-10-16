package io.aether.android

/** What the shell does once the dashboard's renderer is gone. */
enum class RendererLoss {
    /** Android reclaimed it in the background: load the dashboard again once on screen. */
    RELOAD,

    /**
     * It was killed while on screen. Show that, with Retry: reloading on
     * every kill could repeat for as long as whatever kills it does.
     */
    KILLED,

    /** It crashed. Show that, with Retry: reloading would crash it again. */
    CRASHED,

    /** The shell ended it for not responding. Show that, with Retry. */
    UNRESPONSIVE,
}

/**
 * [didCrash] is `RenderProcessGoneDetail.didCrash`, which is false for a
 * renderer ended with `terminate`, so [terminated] says whether the shell
 * ended it itself. [started] is whether the window was on screen. Only a
 * renderer Android reclaimed in the background is reloaded unasked.
 */
fun rendererLoss(didCrash: Boolean, terminated: Boolean, started: Boolean): RendererLoss =
    when {
        terminated -> RendererLoss.UNRESPONSIVE
        didCrash -> RendererLoss.CRASHED
        started -> RendererLoss.KILLED
        else -> RendererLoss.RELOAD
    }

/**
 * Whether a renderer WebView has reported unresponsive [reports] times in a
 * row is ended. WebView reports at most every 5 seconds, so one long task in
 * the page is let finish; a second report in a row is a hang.
 */
fun endsUnresponsiveRenderer(reports: Int): Boolean = reports >= 2

/**
 * Where a reclaimed renderer's dashboard reloads: [base], the address the
 * shell loaded, with the route of [current], the page that was open. The
 * dashboard keeps its route in the query. The open page's own address cannot
 * be loaded as it is: the dashboard took its gateway token off it, and the
 * token it kept went with the renderer. Anything not on [base]'s origin is
 * not the dashboard and leaves [base] as it is.
 */
fun recoveryUrl(base: String, current: String): String {
    if (!isDashboardUrl(base, current)) return base
    val query = current.substringAfter('?', "").substringBefore('#')
    val route = query.split('&').filter { it.isNotEmpty() && it.substringBefore('=') != "token" }
    if (route.isEmpty()) return base
    return base + (if ('?' in base) "&" else "?") + route.joinToString("&")
}
