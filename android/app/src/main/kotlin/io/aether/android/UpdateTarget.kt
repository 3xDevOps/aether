package io.aether.android

import android.content.Context
import android.content.pm.PackageManager
import android.os.Build

/** Where this install's updates come from, read from the installer Android recorded. */
enum class InstallSource {
    /** Google Play installed it, so Play installs its updates. */
    PLAY,

    /** Anything else: the APK from the GitHub release, or `adb install`. */
    RELEASE,
}

// Play's own package, and the one older Play versions recorded installs under.
private val PLAY_INSTALLERS = setOf("com.android.vending", "com.google.android.feedback")

private const val RELEASES = "https://github.com/3xDevOps/Aether/releases/tag/"

// A release tag as the gateway reports it, prerelease included. Nothing else
// is built into a URL.
private val TAG = Regex("""v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?""")

fun installSourceFor(installer: String?): InstallSource =
    if (installer in PLAY_INSTALLERS) InstallSource.PLAY else InstallSource.RELEASE

/**
 * The pages that update this install to [tag], in the order to try them.
 *
 * A Play install opens its listing: the Play app through `market://`, else
 * the listing in the browser. Any other install opens the release page, which
 * carries the signed APK. Null for a [tag] that is not a release tag.
 */
fun updateUrls(source: InstallSource, packageName: String, tag: String): List<String>? {
    if (!TAG.matches(tag)) return null
    return when (source) {
        InstallSource.PLAY ->
            listOf(
                "market://details?id=$packageName",
                "https://play.google.com/store/apps/details?id=$packageName",
            )
        InstallSource.RELEASE -> listOf(RELEASES + tag)
    }
}

/** The package that installed this app, null when Android recorded none. */
fun installerPackage(context: Context): String? =
    try {
        val pm = context.packageManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            pm.getInstallSourceInfo(context.packageName).installingPackageName
        } else {
            @Suppress("DEPRECATION")
            pm.getInstallerPackageName(context.packageName)
        }
    } catch (_: PackageManager.NameNotFoundException) {
        null
    }
