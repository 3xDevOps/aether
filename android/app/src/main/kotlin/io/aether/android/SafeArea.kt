package io.aether.android

// WebView forwards the window's system-bar and display-cutout insets into CSS
// `env(safe-area-inset-*)` from Chromium M136, for a WebView that fills the
// screen - which this shell's does. WebView updates through the Play Store
// independently of the Android version, so this is not something minSdk can
// guarantee.
private const val SAFE_AREA_MILESTONE = 136

/**
 * Whether this device's WebView reports safe-area insets to the page.
 *
 * When it does, the shell hands the insets through and the dashboard pads
 * itself. When it does not - or when the version cannot be read - the shell
 * pads for the system bars itself, because the alternative is a title bar
 * under the status bar on a phone nobody can fix from here.
 */
fun forwardsSafeAreaInsets(webViewVersion: String?): Boolean {
    val milestone = webViewVersion?.substringBefore('.')?.toIntOrNull() ?: return false
    return milestone >= SAFE_AREA_MILESTONE
}
