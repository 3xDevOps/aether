package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private const val PACKAGE = "io.aether.android"

class UpdateTargetTest {
    @Test
    fun playInstallsUpdateFromPlayWhateverTheSigner() {
        for (signed in listOf(true, false)) {
            assertEquals(InstallSource.PLAY, installSourceFor("com.android.vending", signed))
            assertEquals(InstallSource.PLAY, installSourceFor("com.google.android.feedback", signed))
        }
    }

    @Test
    fun onlyTheReleaseKeyTakesTheReleaseApk() {
        for (installer in listOf(null, "com.google.android.packageinstaller", "com.android.chrome", "org.fdroid.fdroid")) {
            assertEquals(installer, InstallSource.RELEASE, installSourceFor(installer, true))
        }
    }

    // Play App Signing re-signs Play builds, so the release APK never installs
    // over one, whether Aurora Store or a split-APK sideload put it there.
    @Test
    fun anyOtherSignerUpdatesFromPlay() {
        for (installer in listOf(null, "com.aurora.store", "com.google.android.packageinstaller")) {
            assertEquals(installer, InstallSource.PLAY, installSourceFor(installer, false))
        }
    }

    @Test
    fun playOpensThePlayAppThenTheListingInTheBrowser() {
        assertEquals(
            listOf(
                UpdateTarget("market://details?id=$PACKAGE", "com.android.vending"),
                UpdateTarget("https://play.google.com/store/apps/details?id=$PACKAGE"),
            ),
            updateTargets(InstallSource.PLAY, PACKAGE, "v0.8.0"),
        )
    }

    @Test
    fun aReleaseInstallDownloadsThatReleasesApk() {
        assertEquals(
            listOf(UpdateTarget("https://github.com/3xDevOps/Aether/releases/download/v0.7.0-alpha.11/aether-android.apk")),
            updateTargets(InstallSource.RELEASE, PACKAGE, "v0.7.0-alpha.11"),
        )
        assertEquals(
            "https://github.com/3xDevOps/Aether/releases/tag/v0.8.0",
            releaseNotesUrl("v0.8.0"),
        )
    }

    // The same tags as selfupdate.ValidTag, hyphens in prerelease identifiers included.
    @Test
    fun releaseTagsMatchTheCli() {
        for (tag in listOf("v0.7.0", "v1.2.3-alpha.11", "v1.2.3-rc-1", "v1.2.3-x-y.z-1.2", "v10.20.30-0")) {
            assertTrue(tag, isReleaseTag(tag))
        }
        for (tag in listOf("", "dev", "0.7.0", "v0.7", "v0.7.0-", "v0.7.0-a..b", "v0.7.0-a.", "v0.7.0+build", "v01.2.3")) {
            assertFalse(tag, isReleaseTag(tag))
        }
    }

    @Test
    fun onlyAReleaseTagBecomesAUrl() {
        for (tag in listOf("", "dev", "v0.7.0/../../evil", "v0.7.0?x=1", "v0.7.0#x", "v0.7.0-")) {
            assertNull(tag, updateTargets(InstallSource.RELEASE, PACKAGE, tag))
            assertNull(tag, updateTargets(InstallSource.PLAY, PACKAGE, tag))
            assertNull(tag, releaseNotesUrl(tag))
        }
    }
}
