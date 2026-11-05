package io.aether.android

import java.net.URI
import java.net.URISyntaxException
import org.json.JSONException
import org.json.JSONObject

/**
 * A decrypted push as the server's internal/push sends it, read the way
 * web/public/sw.js reads it, so the phone shows what a browser would.
 */
data class PushNotice(
    val title: String,
    val body: String,
    /** One notification per tag: a newer push replaces the older. */
    val tag: String,
    val run: String?,
    val workspace: String?,
    val url: String?,
    /** The assistant card the push offers Review… and Decline for. */
    val card: String?,
    /** The need ended: the notification under [tag] goes. */
    val clear: Boolean,
    /** An assistant's news, which a clear leaves shown. */
    val notice: Boolean,
    val quiet: Boolean,
)

enum class PushChannel { NEEDS_YOU, UPDATES }

/**
 * A session's need and an assistant card, private ones included, need the
 * member; an assistant's notice, a budget and a test do not.
 */
fun pushChannel(notice: PushNotice): PushChannel =
    if (notice.card != null || notice.tag.startsWith("card:") || (notice.run != null && !notice.notice)) {
        PushChannel.NEEDS_YOU
    } else {
        PushChannel.UPDATES
    }

enum class PushAction { REVIEW, DECLINE }

// The server offers a card's buttons only when the push shows the whole of
// it, and maxBodyRunes in internal/push is the limit it holds them to.
const val MAX_CARD_RUNES = 240

// Card ids are rand.Text(); a lenient match, since the id is only ever sent
// back to the server inside a JSON body.
private val CARD_ID = Regex("^[A-Za-z0-9_-]{1,128}$")
private val RUN_ID = Regex("^[0-9a-z-]{10,32}$")
private val WORKSPACE_ID = Regex("^[0-9A-Za-z_-]{1,64}$")

private const val FALLBACK_TITLE = "Aether"
private const val FALLBACK_BODY = "A session needs you."

/**
 * The push in [content]. A message this app cannot read still shows the
 * fallback, as the service worker does.
 */
fun parsePush(content: ByteArray): PushNotice {
    val json =
        try {
            JSONObject(String(content, Charsets.UTF_8))
        } catch (_: JSONException) {
            JSONObject()
        }
    fun text(name: String): String? = json.optString(name, "").takeIf { json.opt(name) is String && it.isNotEmpty() }
    val run = text("run")?.takeIf { RUN_ID.matches(it) }
    val workspace = text("workspace")?.takeIf { WORKSPACE_ID.matches(it) }
    val url = text("url")?.takeIf(::isDashboardPath)
    val tag = text("tag")
    val clear = json.optBoolean("clear", false)
    val cleared = tag ?: run ?: url
    return PushNotice(
        title = text("title") ?: FALLBACK_TITLE,
        body = text("body") ?: FALLBACK_BODY,
        tag =
            if (clear && cleared != null) {
                cleared
            } else {
                tag ?: run ?: workspace?.let { "budget:$it" } ?: url ?: "aether"
            },
        run = run,
        workspace = workspace,
        url = url,
        card = text("card")?.takeIf { CARD_ID.matches(it) },
        clear = clear && cleared != null,
        notice = json.optBoolean("notice", false),
        quiet = json.optBoolean("quiet", false),
    )
}

/**
 * The buttons [notice] gets, by the service worker's rules: Review… opens
 * the card, the one place it is approved, and Decline decides it from the
 * notification. A card the server sent without
 * its id is private or long, and gets none; so does one longer than
 * [MAX_CARD_RUNES] whatever the server said. "Always" is never offered.
 *
 * [declinesDirectly] is false where the app has no credential to decide
 * with while it is closed, Aether Edge mode: there the notification only
 * opens the card.
 */
fun pushActions(notice: PushNotice, declinesDirectly: Boolean): List<PushAction> {
    if (notice.card == null || notice.clear || !declinesDirectly) return emptyList()
    if (notice.body.codePointCount(0, notice.body.length) > MAX_CARD_RUNES) return emptyList()
    return listOf(PushAction.REVIEW, PushAction.DECLINE)
}

/** The dashboard path tapping [notice] opens, as the service worker picks it. */
fun pushPath(notice: PushNotice): String =
    notice.url
        ?: notice.run?.let { "/?run=$it" }
        ?: notice.workspace?.let { "/?page=workspace&id=$it" }
        ?: "/"

/**
 * Whether [path] is a dashboard route: `/`, or `/?` and a query. The
 * dashboard routes by query alone, so nothing else is let through to be
 * appended to its address, from a push or from an intent another app sent.
 */
fun isDashboardPath(path: String): Boolean {
    if (path == "/") return true
    if (!path.startsWith("/?") || path.length > 4096) return false
    return path.none { it.isWhitespace() || it.isISOControl() || it == '#' || it == '\\' }
}

/**
 * The `POST /api/v1/<method>` address on [dashboard] and the bearer token it
 * needs: none for a tailnet dashboard, where the phone's tailnet login is
 * the identity, and the gateway's own token for the Edge gateway's
 * `http://127.0.0.1:<port>/?token=<token>` URL.
 */
fun rpcEndpoint(dashboard: String, method: String): Pair<String, String?> {
    val uri = URI(dashboard)
    val origin = "${uri.scheme}://${uri.rawAuthority}"
    val token =
        uri.rawQuery?.split('&')?.firstOrNull { it.startsWith("token=") }?.removePrefix("token=")
    return "$origin/api/v1/$method" to token
}
