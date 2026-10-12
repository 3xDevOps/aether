package io.aether.android

import java.net.URI
import java.net.URISyntaxException
import java.net.URLDecoder
import java.net.URLEncoder

// Run IDs are lowercase letters, digits, and hyphens (run-7k2m9q4xbd).
// Anything else is refused rather than concatenated into the dashboard URL,
// exactly as the desktop shell does.
private val RUN_ID = Regex("^[0-9a-z-]{10,32}$")
private val RELAY_ORIGIN = Regex("^https://[a-z0-9.-]+(:[0-9]{1,5})?$")
private val SERVER_ID = Regex("^[a-z2-7]{26}$")
private val INVITATION_ID = Regex("^inv-[0-9a-hjkmnp-tv-z]{10}$")
private val SECRET = Regex("^[a-z2-7]{16}$")

/**
 * The dashboard URL an `aether://run/<id>`, `aether://join?…#<secret>` or
 * `aether://claim?…#<secret>` link opens, or null when the link is none of
 * them.
 *
 * `base` is a dashboard URL from [dashboardUrl], so it always ends in `/` and
 * carries no query of its own.
 */
fun deepLinkUrl(base: String, link: String): String? {
    val uri =
        try {
            URI(link)
        } catch (_: URISyntaxException) {
            return null
        }
    if (uri.scheme?.lowercase() != "aether") return null
    return when (uri.host) {
        // aether://run/<id> parses as host "run", path "/<id>".
        "run" -> {
            val id = uri.path?.removePrefix("/") ?: return null
            if (RUN_ID.matches(id)) "$base?run=$id" else null
        }
        "join" -> edgeLink(base, uri, link, listOf("edge", "server", "invitation"))
        "claim" -> edgeLink(base, uri, link, listOf("edge", "server"))
        else -> null
    }
}

private fun edgeLink(base: String, uri: URI, link: String, names: List<String>): String? {
    if (!uri.path.isNullOrEmpty() && uri.path != "/") return null
    if (!SECRET.matches(uri.rawFragment ?: return null)) return null
    val pairs =
        try {
            (uri.rawQuery ?: return null).split("&").map {
                val (name, value) = it.split("=", limit = 2).takeIf { p -> p.size == 2 } ?: return null
                URLDecoder.decode(name, "UTF-8") to URLDecoder.decode(value, "UTF-8")
            }
        } catch (_: IllegalArgumentException) {
            return null
        }
    val params = pairs.toMap()
    if (pairs.size != names.size || params.keys != names.toSet()) return null
    if (!RELAY_ORIGIN.matches(params.getValue("edge"))) return null
    if (!SERVER_ID.matches(params.getValue("server"))) return null
    if ("invitation" in params && !INVITATION_ID.matches(params.getValue("invitation"))) return null
    return "$base?${uri.host}=${URLEncoder.encode(link, "UTF-8")}"
}
