package io.aether.android

import java.net.URI
import java.net.URISyntaxException

// Run IDs are lowercase ULIDs. Anything else is refused rather than
// concatenated into the dashboard URL, exactly as the desktop shell does.
private val RUN_ID = Regex("^[0-9a-z]{10,32}$")

/**
 * The dashboard URL an `aether://run/<id>` link opens, or null when the link
 * is not one.
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
    // aether://run/<id> parses as host "run", path "/<id>".
    if (uri.host != "run") return null
    val id = uri.path?.removePrefix("/") ?: return null
    if (!RUN_ID.matches(id)) return null
    return "$base?run=$id"
}
