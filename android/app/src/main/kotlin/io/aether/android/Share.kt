package io.aether.android

import java.net.URLEncoder

// A URL a WebView, the gateway and the server all take, with room for a
// prompt someone actually shares.
const val MAX_SHARED_CHARS = 8000

/**
 * The dashboard path a shared text or link opens: the new-session flow with
 * it as the prompt, `/?new=1&prompt=<text>`. Null for nothing to share. The
 * text is cut to [MAX_SHARED_CHARS], never through a surrogate pair.
 */
fun sharePath(text: String?): String? {
    val trimmed = text?.trim()?.takeIf { it.isNotEmpty() } ?: return null
    var end = minOf(trimmed.length, MAX_SHARED_CHARS)
    if (end < trimmed.length && Character.isHighSurrogate(trimmed[end - 1])) end--
    // encodeURIComponent, which URLEncoder is but for the space.
    val encoded = URLEncoder.encode(trimmed.substring(0, end), "UTF-8").replace("+", "%20")
    return "/?new=1&prompt=$encoded"
}

/**
 * What a share carries: its text, with its subject on the line before when
 * the text does not already hold it, as a page shared from a browser has
 * its title as the subject and its address as the text.
 */
fun sharedText(text: String?, subject: String?): String? {
    val body = text?.trim().orEmpty()
    val title = subject?.trim().orEmpty()
    return when {
        title.isEmpty() -> body.ifEmpty { null }
        body.isEmpty() || body.contains(title) -> body.ifEmpty { title }
        else -> "$title\n$body"
    }
}
