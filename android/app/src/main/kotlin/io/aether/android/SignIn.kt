package io.aether.android

import androidx.annotation.StringRes
import java.net.URI
import java.net.URISyntaxException

// The dashboard's sign-in through an edge, and the parameter that asks the
// edge to return to this app (internal/edgeproto/api.go).
private const val LOGIN_PATH = "/auth/login"
private const val CALLBACK_PATH = "/auth/callback"
private const val RETURN_APP = "return=app"

// The longest link the edge sends is well under this: two parameter names, a
// 43-character code and a state of at most 256 characters.
private const val MAX_LINK_LENGTH = 512

// Neither value ever needs percent-encoding, so a link carrying any is not
// one the edge sent. The code is a token, 43 base64url characters; the state
// is what the edge accepts from the server.
private val CODE = Regex("^[A-Za-z0-9_-]{43}$")
private val STATE = Regex("^[A-Za-z0-9._~-]{1,256}$")

// An edge dashboard's host name is <server id>.<server domain>, and a server
// id is 26 lowercase base32 characters.
private val SERVER_ID = Regex("^[a-z2-7]{26}$")

private val SIGN_IN_LINK = Regex("^aether://auth([/?#]|$)", RegexOption.IGNORE_CASE)

/**
 * What an `aether://auth/callback` link was refused for, resolved against
 * `strings.xml` by the screen that shows it.
 */
class SignInLinkError(@get:StringRes val reason: Int, vararg val detail: String) :
    IllegalArgumentException()

/**
 * The address to load instead when the page navigates to the dashboard's
 * sign-in, or null for every other navigation.
 *
 * Google refuses OAuth inside a WebView, so the edge's sign-in page has to
 * open in the phone's browser and then come back to this app, not to the
 * server. `return=app` makes the server ask the edge for that. The server
 * answers with the state cookie and a redirect off the dashboard's origin,
 * which [navigationFor] hands to the browser; the cookie stays in the
 * WebView.
 */
fun appSignInUrl(base: String?, url: String): String? {
    if (base == null || !isDashboardUrl(base, url)) return null
    // isDashboardUrl has parsed it already.
    val uri = URI(url)
    if (uri.rawPath != LOGIN_PATH) return null
    val query = uri.rawQuery
    if (query != null && RETURN_APP in query.split('&')) return null
    return base.removeSuffix("/") + LOGIN_PATH + "?" + (query?.let { "$it&" } ?: "") + RETURN_APP
}

/** Whether [link] is addressed to `aether://auth`, well formed or not. */
fun isSignInLink(link: String): Boolean = SIGN_IN_LINK.containsMatchIn(link)

/**
 * The dashboard address that completes the sign-in an
 * `aether://auth/callback?code=…&state=…` link returns from.
 *
 * The host is always the saved dashboard's, never one from the link. The code
 * is bound to the state cookie and the verifier of the browser that started
 * the sign-in. For a sign-in this app started, that is this WebView, and a
 * link another app intercepts is useless to it. For a sign-in an attacker
 * started in their own browser and sent to a member as an edge link, it is
 * the attacker's: another app that claims `aether://` can take the code and
 * hand it to them. The link is checked to be exactly what the edge sends,
 * and never logged.
 *
 * @throws SignInLinkError when the link is not exactly that, or [base] is not
 *   a dashboard reached through an edge.
 */
fun signInReturnUrl(base: String, link: String): String {
    if (link.length > MAX_LINK_LENGTH) throw SignInLinkError(R.string.sign_in_link_error_shape)
    val uri =
        try {
            URI(link)
        } catch (_: URISyntaxException) {
            throw SignInLinkError(R.string.sign_in_link_error_shape)
        }
    // The raw authority is "auth" only with no user info and no port.
    if (
        uri.scheme?.lowercase() != "aether" ||
        uri.rawAuthority != "auth" ||
        uri.rawPath != "/callback" ||
        uri.rawFragment != null
    ) {
        throw SignInLinkError(R.string.sign_in_link_error_shape)
    }
    val params = uri.rawQuery?.split('&')?.map { it.split('=', limit = 2) }.orEmpty()
    if (params.size != 2 || params.any { it.size != 2 }) {
        throw SignInLinkError(R.string.sign_in_link_error_shape)
    }
    val values = params.associate { it[0] to it[1] }
    val code = values["code"]
    val state = values["state"]
    if (code == null || state == null) throw SignInLinkError(R.string.sign_in_link_error_shape)
    if (!CODE.matches(code) || !STATE.matches(state)) {
        throw SignInLinkError(R.string.sign_in_link_error_values)
    }
    val host = URI(base).host.orEmpty()
    if ('.' !in host || !SERVER_ID.matches(host.lowercase().substringBefore('.'))) {
        throw SignInLinkError(R.string.sign_in_link_error_not_edge, host)
    }
    return base.removeSuffix("/") + CALLBACK_PATH + "?code=$code&state=$state"
}
