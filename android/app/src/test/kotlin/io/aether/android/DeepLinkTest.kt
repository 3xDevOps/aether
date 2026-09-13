package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

private const val BASE = "https://my-server.ts.net/"

class DeepLinkTest {
    @Test
    fun runLinkOpensThatRunOnTheDashboard() {
        assertEquals("$BASE?run=01k4x7m2qc", deepLinkUrl(BASE, "aether://run/01k4x7m2qc"))
    }

    @Test
    fun schemeCaseIsAcceptedBecauseAndroidLowercasesItAnyway() {
        assertEquals("$BASE?run=01k4x7m2qc", deepLinkUrl(BASE, "AETHER://run/01k4x7m2qc"))
    }

    @Test
    fun otherSchemesAndHostsAreNotRunLinks() {
        assertNull(deepLinkUrl(BASE, "https://my-server.ts.net/"))
        assertNull(deepLinkUrl(BASE, "aether://workspace/01k4x7m2qc"))
        assertNull(deepLinkUrl(BASE, "not a url"))
    }

    @Test
    fun anIdThatIsNotARunIdIsRefused() {
        assertNull(deepLinkUrl(BASE, "aether://run/"))
        assertNull(deepLinkUrl(BASE, "aether://run/short"))
        assertNull(deepLinkUrl(BASE, "aether://run/01K4X7M2QC"))
        assertNull(deepLinkUrl(BASE, "aether://run/01k4x7m2qc/../../etc"))
        assertNull(deepLinkUrl(BASE, "aether://run/01k4x7m2qc&token=x"))
    }
}
