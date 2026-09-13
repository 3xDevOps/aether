package io.aether.android

import java.net.URI
import java.net.URISyntaxException

/** What a typed server address was rejected for, in words the setup screen shows. */
class ServerAddressError(message: String) : IllegalArgumentException(message)

/**
 * The dashboard URL a typed server address points at.
 *
 * The server hosts the dashboard on the MagicDNS name of its tailnet node,
 * over HTTPS only, and answers at the root (docs/networking.md). So a bare
 * name is enough, a pasted URL is accepted, everything after the authority is
 * dropped, and cleartext is refused rather than silently upgraded: a shell
 * that accepted `http://` would carry a member's whole authority in clear.
 */
fun dashboardUrl(raw: String): String {
    val typed = raw.trim()
    if (typed.isEmpty()) {
        throw ServerAddressError("Enter the server's tailnet name, for example my-server.tailnet-name.ts.net")
    }
    // A bare MagicDNS name has no scheme, and URI would read `host:443` as
    // one, so the scheme is supplied before parsing rather than guessed after.
    val absolute = if (typed.contains("://")) typed else "https://$typed"
    val uri =
        try {
            URI(absolute)
        } catch (e: URISyntaxException) {
            throw ServerAddressError("Not a server address: ${e.reason}")
        }

    val scheme = uri.scheme?.lowercase()
    if (scheme != "https") {
        throw ServerAddressError(
            "The dashboard is served over HTTPS only. Enter the server's tailnet name, or an https:// address.",
        )
    }
    if (uri.userInfo != null) {
        throw ServerAddressError("A server address carries no username or password.")
    }
    val host = uri.host
    if (host.isNullOrEmpty()) {
        throw ServerAddressError("No server name in \"$typed\".")
    }
    val port = uri.port
    if (port == 0 || port > 65535) {
        throw ServerAddressError("Port $port is not a port.")
    }

    val authority = if (port == -1) host else "$host:$port"
    return "https://$authority/"
}

/**
 * Whether [target] is on the same origin as the dashboard at [base].
 *
 * The WebView is a browser locked to one origin; everything else - an agent's
 * OAuth page, a link in a run summary - goes to the system browser. An
 * unparseable or schemeless target is not the dashboard.
 */
fun isDashboardUrl(base: String, target: String): Boolean {
    val here = origin(base) ?: return false
    return origin(target) == here
}

private fun origin(url: String): String? {
    // java.net.URI refuses characters Chromium leaves unescaped in a query or
    // fragment ("[", "]", "|", "^", "{", "}"), so a same-origin link carrying
    // one reads as off-origin and opens in the phone's browser. No dashboard
    // URL carries them - its routing lives in the store, not the path. The
    // fix, if one ever does, is to percent-encode before parsing: android.net
    // .Uri would parse them but is not available to these unit tests.
    val uri =
        try {
            URI(url)
        } catch (_: URISyntaxException) {
            return null
        }
    val scheme = uri.scheme?.lowercase() ?: return null
    val host = uri.host?.lowercase() ?: return null
    val port = if (uri.port == -1) defaultPort(scheme) else uri.port
    return "$scheme://$host:$port"
}

private fun defaultPort(scheme: String): Int =
    when (scheme) {
        "https" -> 443
        "http" -> 80
        else -> -1
    }
