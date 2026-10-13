package io.aether.android

import android.content.Context

private const val PREFS = "aether"
private const val DASHBOARD_URL = "dashboard_url"
private const val MODE = "connection_mode"
private const val GATEWAY_PORT = "gateway_port"

/** How the shell reaches a server. */
enum class ConnectionMode(val stored: String) {
    /** Through Aether Edge: the bundled CLI serves the dashboard on loopback. */
    EDGE("edge"),

    /** Straight to the server's HTTPS dashboard on the phone's tailnet. */
    TAILSCALE("tailscale"),
}

/**
 * The mode the stored values mean, or null when the shell is not set up.
 *
 * An install from before Aether Edge stored only the dashboard address, and
 * keeps working as a Tailscale one.
 */
fun resolveMode(mode: String?, dashboardUrl: String?): ConnectionMode? =
    ConnectionMode.entries.firstOrNull { it.stored == mode }
        ?: if (dashboardUrl != null) ConnectionMode.TAILSCALE else null

/**
 * The only state the shell itself keeps. It stores no credential: in
 * Tailscale mode the phone's tailnet login is the identity
 * (docs/security.md), and in Edge mode the CLI sidecar keeps the device key
 * and tokens in app-private storage, out of this file.
 */
fun Context.connectionMode(): ConnectionMode? =
    resolveMode(prefs().getString(MODE, null), storedDashboardUrl())

fun Context.storeConnectionMode(mode: ConnectionMode) {
    prefs().edit().putString(MODE, mode.stored).apply()
}

fun Context.storedDashboardUrl(): String? = prefs().getString(DASHBOARD_URL, null)

/** Stores the address and switches to Tailscale mode in one write. */
fun Context.storeDashboardUrl(url: String) {
    prefs()
        .edit()
        .putString(DASHBOARD_URL, url)
        .putString(MODE, ConnectionMode.TAILSCALE.stored)
        .apply()
}

/**
 * The loopback port the Edge gateway asks for, picked once. The dashboard
 * keeps its settings in storage tied to its origin, port included, so a new
 * port on every start would forget them.
 */
fun Context.gatewayPort(): Int {
    val stored = prefs().getInt(GATEWAY_PORT, 0)
    if (stored != 0) return stored
    val port = (20000..59999).random()
    prefs().edit().putInt(GATEWAY_PORT, port).apply()
    return port
}

private fun Context.prefs() = getSharedPreferences(PREFS, Context.MODE_PRIVATE)
