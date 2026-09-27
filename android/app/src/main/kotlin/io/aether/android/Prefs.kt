package io.aether.android

import android.content.Context

private const val PREFS = "aether"
private const val DASHBOARD_URL = "dashboard_url"

/**
 * The only state the shell itself keeps. It stores no credential. Through an
 * edge, the dashboard's session cookie is kept by the WebView's cookie store,
 * not here; on a tailnet there is none, because the phone's tailnet login is
 * the identity (docs/security.md).
 */
fun Context.storedDashboardUrl(): String? =
    getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(DASHBOARD_URL, null)

fun Context.storeDashboardUrl(url: String) {
    getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(DASHBOARD_URL, url).apply()
}
