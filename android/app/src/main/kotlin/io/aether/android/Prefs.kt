package io.aether.android

import android.content.Context

private const val PREFS = "aether"
private const val DASHBOARD_URL = "dashboard_url"

/**
 * The only state the shell keeps. No credential is stored: the phone's
 * tailnet login is the identity (docs/security.md).
 */
fun Context.storedDashboardUrl(): String? =
    getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(DASHBOARD_URL, null)

fun Context.storeDashboardUrl(url: String) {
    getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(DASHBOARD_URL, url).apply()
}
