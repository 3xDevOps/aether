package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ServerAddressTest {
    @Test
    fun bareMagicDnsNameBecomesAnHttpsRoot() {
        assertEquals(
            "https://my-server.tailnet-name.ts.net/",
            dashboardUrl("my-server.tailnet-name.ts.net"),
        )
    }

    @Test
    fun pastedUrlKeepsOnlyItsAuthority() {
        assertEquals(
            "https://my-server.tailnet-name.ts.net/",
            dashboardUrl("https://my-server.tailnet-name.ts.net/?run=01abcdefgh#x"),
        )
    }

    @Test
    fun surroundingWhitespaceAndSchemeCaseAreAccepted() {
        assertEquals(
            "https://my-server.tailnet-name.ts.net/",
            dashboardUrl("  HTTPS://my-server.tailnet-name.ts.net  "),
        )
    }

    @Test
    fun anExplicitPortIsKept() {
        assertEquals("https://my-server.ts.net:8443/", dashboardUrl("my-server.ts.net:8443"))
        assertEquals("https://my-server.ts.net:8443/", dashboardUrl("https://my-server.ts.net:8443"))
    }

    @Test
    fun cleartextIsRefusedRatherThanUpgraded() {
        val error =
            runCatching { dashboardUrl("http://my-server.ts.net") }.exceptionOrNull()
        assertTrue(error is ServerAddressError)
        assertTrue(error!!.message!!.contains("HTTPS only"))
    }

    @Test
    fun emptyAndMalformedInputAreRefused() {
        assertTrue(runCatching { dashboardUrl("   ") }.exceptionOrNull() is ServerAddressError)
        assertTrue(runCatching { dashboardUrl("my server") }.exceptionOrNull() is ServerAddressError)
        assertTrue(runCatching { dashboardUrl("https://") }.exceptionOrNull() is ServerAddressError)
        assertTrue(runCatching { dashboardUrl("ssh://my-server.ts.net") }.exceptionOrNull() is ServerAddressError)
    }

    @Test
    fun credentialsInTheAddressAreRefused() {
        assertTrue(
            runCatching { dashboardUrl("https://me:secret@my-server.ts.net") }.exceptionOrNull()
                is ServerAddressError,
        )
    }

    @Test
    fun onlyTheDashboardOriginStaysInTheWebView() {
        val base = "https://my-server.ts.net/"
        assertTrue(isDashboardUrl(base, "https://my-server.ts.net/api/v1/run.list"))
        assertTrue(isDashboardUrl(base, "https://MY-SERVER.ts.net:443/"))
        assertFalse(isDashboardUrl(base, "http://my-server.ts.net/"))
        assertFalse(isDashboardUrl(base, "https://my-server.ts.net:8443/"))
        assertFalse(isDashboardUrl(base, "https://github.com/login/oauth/authorize"))
        assertFalse(isDashboardUrl(base, "javascript:alert(1)"))
        assertFalse(isDashboardUrl(base, "not a url"))
    }

    @Test
    fun anExplicitPortDashboardMatchesItsOwnPort() {
        val base = "https://my-server.ts.net:8443/"
        assertTrue(isDashboardUrl(base, "https://my-server.ts.net:8443/"))
        assertFalse(isDashboardUrl(base, "https://my-server.ts.net/"))
    }
}
