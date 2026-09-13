package io.aether.android

// The version this shell trusts to report safe areas to the page. Two sources
// disagree on where that starts. "Understand window insets in WebView"
// (developer.android.com/develop/ui/views/layout/webapps/understand-window-insets)
// puts systemBars and displayCutout forwarding at M136 for a WebView that
// fills the screen, M144 for all of them; the capacitor-community/safe-area
// plugin pads natively below 140, citing Chromium issue 40699457, "Chromium
// versions < 140 do not correctly report safe area insets".
//
// 140 is the safe reading of that disagreement because the risk is not
// symmetric. Trusting a WebView that does not in fact report insets puts the
// dashboard's title bar under the status bar with no way for the member to
// fix it; distrusting one that does costs only the edge-to-edge look. Nobody
// has run this shell on a 136-139 WebView, so do not lower this without one.
private const val SAFE_AREA_MILESTONE = 140

/**
 * Whether this device's WebView reports safe-area insets to the page.
 *
 * When it does, the shell hands the insets through and the dashboard pads
 * itself with `env(safe-area-inset-*)`. When it does not - or when the
 * version cannot be read - the shell pads for the system bars itself.
 */
fun forwardsSafeAreaInsets(webViewVersion: String?): Boolean {
    val milestone = webViewVersion?.substringBefore('.')?.toIntOrNull() ?: return false
    return milestone >= SAFE_AREA_MILESTONE
}
