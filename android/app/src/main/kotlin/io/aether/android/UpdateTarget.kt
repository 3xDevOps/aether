package io.aether.android

import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import java.security.MessageDigest

/** Where this install's updates come from. */
enum class InstallSource {
    /** Google Play installed it, or Play App Signing signed it, so Play updates it. */
    PLAY,

    /** Signed with the release key, so the APK on a GitHub release installs over it. */
    RELEASE,
}

const val PLAY_PACKAGE = "com.android.vending"

// Play's own package, and the one older Play versions recorded installs under.
private val PLAY_INSTALLERS = setOf(PLAY_PACKAGE, "com.google.android.feedback")

// SHA-256 of the release keystore's certificate, as `apksigner verify
// --print-certs` prints it for aether-android.apk on every GitHub release.
// Play serves builds re-signed with its own app signing key
// (android/listing/README.md), which never matches.
private const val RELEASE_CERT_SHA256 = "2d7e0777aa8a24ac0090dcdd4c620910c9ef13c7775b1576ed69230f1dd37c3a"

private const val RELEASES = "https://github.com/3xDevOps/Aether/releases/tag/"

// selfupdate.ValidTag: nothing else is built into a URL.
private val TAG = Regex("""v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?""")

fun isReleaseTag(tag: String): Boolean = TAG.matches(tag)

/**
 * Only an app signed with the release key takes the release APK. Any other
 * signer is most likely Play App Signing, whichever store installed it.
 */
fun installSourceFor(installer: String?, releaseSigned: Boolean): InstallSource =
    if (installer !in PLAY_INSTALLERS && releaseSigned) InstallSource.RELEASE else InstallSource.PLAY

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

/** What decides this install's updates, read once: an app update restarts the process. */
class InstallFacts(val source: InstallSource, val updatedAt: Long)

private var installFacts: InstallFacts? = null

/** Blocks on package manager calls; never call it on the main thread. */
@Synchronized
fun installFacts(context: Context): InstallFacts =
    installFacts ?: run {
        val pm = context.packageManager
        // The flags overload is the only one before API 33.
        @Suppress("DEPRECATION")
        val updatedAt = pm.getPackageInfo(context.packageName, 0).lastUpdateTime
        InstallFacts(installSourceFor(installerPackage(context), releaseSigned(context)), updatedAt)
            .also { installFacts = it }
    }

private fun releaseSigned(context: Context): Boolean {
    val pm = context.packageManager
    val digest = RELEASE_CERT_SHA256.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
        return pm.hasSigningCertificate(context.packageName, digest, PackageManager.CERT_INPUT_SHA256)
    }
    @Suppress("DEPRECATION")
    val signatures = pm.getPackageInfo(context.packageName, PackageManager.GET_SIGNATURES).signatures
    return signatures?.size == 1 &&
        MessageDigest.getInstance("SHA-256").digest(signatures[0].toByteArray()).contentEquals(digest)
}

/** The package that installed this app, null when Android recorded none. */
private fun installerPackage(context: Context): String? =
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
