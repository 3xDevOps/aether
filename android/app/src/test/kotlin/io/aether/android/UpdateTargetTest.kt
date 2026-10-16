package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

private const val PACKAGE = "io.aether.android"

class UpdateTargetTest {
    @Test
    fun playInstallsUpdateFromPlay() {
        assertEquals(InstallSource.PLAY, installSourceFor("com.android.vending"))
        assertEquals(InstallSource.PLAY, installSourceFor("com.google.android.feedback"))
    }

    @Test
    fun everyOtherInstallUpdatesFromTheRelease() {
        assertEquals(InstallSource.RELEASE, installSourceFor(null))
        assertEquals(InstallSource.RELEASE, installSourceFor("com.google.android.packageinstaller"))
        assertEquals(InstallSource.RELEASE, installSourceFor("com.android.chrome"))
        assertEquals(InstallSource.RELEASE, installSourceFor("org.fdroid.fdroid"))
    }

    @Test
    fun playOpensTheAppThenTheListingInTheBrowser() {
        assertEquals(
            listOf(
                "market://details?id=$PACKAGE",
                "https://play.google.com/store/apps/details?id=$PACKAGE",
            ),
            updateUrls(InstallSource.PLAY, PACKAGE, "v0.8.0"),
        )
    }

    @Test
    fun aReleaseInstallOpensThatReleasePage() {
        assertEquals(
            listOf("https://github.com/3xDevOps/Aether/releases/tag/v0.7.0-alpha.11"),
            updateUrls(InstallSource.RELEASE, PACKAGE, "v0.7.0-alpha.11"),
        )
    }

    @Test
    fun onlyAReleaseTagBecomesAUrl() {
        for (tag in listOf("", "dev", "0.7.0", "v0.7", "v0.7.0/../../evil", "v0.7.0?x=1", "v0.7.0#x", "v0.7.0-")) {
            assertNull(tag, updateUrls(InstallSource.RELEASE, PACKAGE, tag))
            assertNull(tag, updateUrls(InstallSource.PLAY, PACKAGE, tag))
        }
    }
}
