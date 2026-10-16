package io.aether.android

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.webkit.JavascriptInterface

/**
 * `window.aetherAndroid` in the Edge mode dashboard: tells the update notice
 * where this install's updates come from, and opens that page when the
 * member asks. It never downloads or installs anything; Play or the browser
 * does that, and the member decides there.
 *
 * The page passes a tag, never a URL, so the most a page can do with this is
 * open the Play listing or a release page of this repository.
 */
class UpdateBridge(private val activity: Activity) {
    private val source = installSourceFor(installerPackage(activity))

    /** `play` or `release`. */
    @JavascriptInterface
    fun updateSource(): String = source.name.lowercase()

    /** Opens the page that updates to [tag]; returns why it could not, or "" when it opened. */
    @JavascriptInterface
    fun openUpdate(tag: String): String {
        val urls = updateUrls(source, activity.packageName, tag) ?: return "not a release tag: $tag"
        val failures = mutableListOf<String>()
        for (url in urls) {
            val intent =
                Intent(Intent.ACTION_VIEW, Uri.parse(url))
                    .addCategory(Intent.CATEGORY_BROWSABLE)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            try {
                activity.startActivity(intent)
                return ""
            } catch (e: ActivityNotFoundException) {
                failures += "$url: ${e.message ?: "no app on this phone opens it"}"
            } catch (e: SecurityException) {
                failures += "$url: ${e.message}"
            }
        }
        return "could not open ${failures.joinToString("; ")}"
    }

    companion object {
        const val NAME = "aetherAndroid"
    }
}
