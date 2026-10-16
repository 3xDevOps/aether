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

const val PLAY_PACKAGE = "com.android.vending"

// Play's own package, and the one older Play versions recorded installs under.
private val PLAY_INSTALLERS = setOf(PLAY_PACKAGE, "com.google.android.feedback")

private const val RELEASES = "https://github.com/3xDevOps/Aether/releases/tag/"

// selfupdate.ValidTag: nothing else is built into a URL.
private val TAG = Regex("""v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?""")

fun isReleaseTag(tag: String): Boolean = TAG.matches(tag)

fun installSourceFor(installer: String?): InstallSource =
    if (installer in PLAY_INSTALLERS) InstallSource.PLAY else InstallSource.RELEASE

/** A page to open, in the app [packageName] when it is set. */
data class UpdateTarget(val url: String, val packageName: String? = null)

/**
 * The pages that update this install to [tag], in the order to try them.
 *
 * A Play install opens its listing: in the Play app, else in the browser. Any
 * other install opens the release page, which carries the signed APK. Null
 * for a [tag] that is not a release tag.
 */
fun updateTargets(source: InstallSource, packageName: String, tag: String): List<UpdateTarget>? {
    if (!isReleaseTag(tag)) return null
    return when (source) {
        InstallSource.PLAY ->
            listOf(
                UpdateTarget("market://details?id=$packageName", PLAY_PACKAGE),
                UpdateTarget("https://play.google.com/store/apps/details?id=$packageName"),
            )
        InstallSource.RELEASE -> listOf(UpdateTarget(RELEASES + tag))
    }
}

fun releaseNotesUrl(tag: String): String? = if (isReleaseTag(tag)) RELEASES + tag else null

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
