package io.aether.android

import java.net.URI
import java.net.URISyntaxException
import java.net.URLDecoder
import java.net.URLEncoder

// Run IDs are lowercase letters, digits, and hyphens (run-7k2m9q4xbd).
// Anything else is refused rather than concatenated into the dashboard URL,
// exactly as the desktop shell does.
private val RUN_ID = Regex("^[0-9a-z-]{10,32}$")

private val SERVER_ID = Regex("^[a-z2-7]{26}$")
private val INVITATION = Regex("^inv-[0-9a-hjkmnp-tv-z]{10}$")
private val LINK_SECRET = Regex("^[a-z2-7]{16}$")
private val HOSTNAME = Regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$")
private val DOTTED_QUAD = Regex("^(0|[1-9]\\d{0,2})(\\.(0|[1-9]\\d{0,2})){3}$")
private val LOOPBACK_V4 = Regex("^127\\.\\d+\\.\\d+\\.\\d+$")
private val LOOPBACK_V6 = Regex("^\\[::1]$|^\\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}]$")
private val EDGE_ORIGIN = Regex("^(https?)://(\\[[0-9a-f:]+]|[^:/?#\\[\\]@]+)(?::(\\d+))?$")

/**
 * The dashboard URL an `aether://run/<id>` link opens in Tailscale mode, or
 * null when the link is not one.
 *
 * `base` is a dashboard URL from [dashboardUrl], so it always ends in `/` and
 * carries no query of its own.
 */
fun deepLinkUrl(base: String, link: String): String? {
    val id = runLinkId(link) ?: return null
    return "$base?run=$id"
}

private fun runLinkId(link: String): String? {
    val uri =
        try {
            URI(link)
        } catch (_: URISyntaxException) {
            return null
        }
    if (uri.scheme?.lowercase() != "aether") return null
    // aether://run/<id> parses as host "run", path "/<id>".
    if (uri.host != "run") return null
    val id = uri.path?.removePrefix("/") ?: return null
    return id.takeIf { RUN_ID.matches(it) }
}

/**
 * Whether [link] is an `aether://join` or `aether://claim` link, which only
 * Edge mode can act on.
 */
fun isEdgeOnlyLink(link: String): Boolean = linkParts(link)?.host in EDGE_LINK_NAMES.keys

private val EDGE_LINK_NAMES =
    mapOf("join" to listOf("edge", "server", "invitation"), "claim" to listOf("edge", "server"))

/**
 * What an `aether://` link appends to the gateway URL in Edge mode, or null
 * when it is not a link the dashboard should see: `&run=<id>`, or
 * `#join=<link>` / `#claim=<link>`. The fragment keeps the link's secret out
 * of the request to the gateway.
 *
 * Every part of a join or claim link is checked before any of it reaches the
 * gateway URL, by the same rules as the desktop shell's `deepLinkSuffix`.
 * Where the two differ this one refuses more, never less: an IPv6 edge host
 * other than loopback, which `URL` would re-serialize and this does not.
 */
fun edgeDeepLinkSuffix(link: String): String? {
    runLinkId(link)?.let { return "&run=$it" }
    val parts = linkParts(link) ?: return null
    val names = EDGE_LINK_NAMES[parts.host] ?: return null
    if (parts.path != "" && parts.path != "/") return null
    val params = queryParams(parts.query) ?: return null
    if (params.size != names.size || names.any { name -> params.count { it.first == name } != 1 }) {
        return null
    }
    fun value(name: String) = params.first { it.first == name }.second
    if (!validEdgeOrigin(value("edge"))) return null
    if (!SERVER_ID.matches(value("server"))) return null
    if ("invitation" in names && !INVITATION.matches(value("invitation"))) return null
    if (parts.fragment == null || !LINK_SECRET.matches(parts.fragment)) return null
    // URLEncoder is form encoding and encodeURIComponent is not: they differ
    // on a space, "*", "~" and "!'()", none of which a validated link holds.
    val encoded = URLEncoder.encode("aether://${link.substring("aether://".length)}", "UTF-8")
    return "#${parts.host}=${encoded.replace("+", "%20")}"
}

private class LinkParts(
    val host: String,
    val path: String,
    val query: String,
    val fragment: String?,
)

// Split by hand: java.net.URI refuses the "[" and "]" an IPv6 edge origin
// carries in the query, and the host of a non-special scheme keeps its case.
private fun linkParts(link: String): LinkParts? {
    if (!link.startsWith("aether://", ignoreCase = true)) return null
    var rest = link.substring("aether://".length)
    val hash = rest.indexOf('#')
    val fragment = if (hash >= 0) rest.substring(hash + 1) else null
    if (hash >= 0) rest = rest.substring(0, hash)
    val question = rest.indexOf('?')
    val query = if (question >= 0) rest.substring(question + 1) else ""
    if (question >= 0) rest = rest.substring(0, question)
    val slash = rest.indexOf('/')
    return if (slash >= 0) {
        LinkParts(rest.substring(0, slash), rest.substring(slash), query, fragment)
    } else {
        LinkParts(rest, "", query, fragment)
    }
}

// URLSearchParams semantics: "+" is a space, a name without "=" has an empty
// value. A malformed escape is refused; no valid value contains one.
private fun queryParams(query: String): List<Pair<String, String>>? =
    try {
        if (query.isEmpty()) {
            emptyList()
        } else {
            query.split('&').map { pair ->
                val eq = pair.indexOf('=')
                val name = if (eq >= 0) pair.substring(0, eq) else pair
                val value = if (eq >= 0) pair.substring(eq + 1) else ""
                URLDecoder.decode(name, "UTF-8") to URLDecoder.decode(value, "UTF-8")
            }
        }
    } catch (_: IllegalArgumentException) {
        null
    }

// A relay origin as edgeproto.Origin accepts it: https://host[:port], or
// http:// for a loopback host, nothing else. The text must already be the
// canonical form of its origin, with an explicit default port the one
// variation, which edgeproto.Origin keeps.
private fun validEdgeOrigin(origin: String): Boolean {
    val match = EDGE_ORIGIN.matchEntire(origin) ?: return false
    val (scheme, host, port) = match.destructured
    val bracketed = host.startsWith("[")
    if (!bracketed) {
        if (!HOSTNAME.matches(host)) return false
        // A numeric last label is an IPv4 address and must be written as one.
        if (host.substringAfterLast('.').all { it.isDigit() } && !DOTTED_QUAD.matches(host)) {
            return false
        }
        if (DOTTED_QUAD.matches(host) && host.split('.').any { it.toInt() > 255 }) return false
    } else if (!LOOPBACK_V6.matches(host)) {
        return false
    }
    val loopback = host == "localhost" || LOOPBACK_V4.matches(host) || LOOPBACK_V6.matches(host)
    if (scheme == "http" && !loopback) return false
    if (port.isEmpty()) return true
    // Digits only, no leading zero: URL would rewrite anything else.
    val number = port.toIntOrNull() ?: return false
    return number in 1..65535 && port == number.toString()
}
