package io.aether.android

import org.junit.Assert.assertArrayEquals
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

    private fun rejection(raw: String): ServerAddressError =
        runCatching { dashboardUrl(raw) }.exceptionOrNull() as ServerAddressError

    @Test
    fun cleartextIsRefusedRatherThanUpgraded() {
        assertEquals(
            R.string.address_error_https_only,
            rejection("http://my-server.ts.net").reason,
        )
    }

    @Test
    fun emptyAndMalformedInputAreRefused() {
        assertEquals(R.string.address_error_empty, rejection("   ").reason)
        assertEquals(R.string.address_error_unparseable, rejection("my server").reason)
        assertEquals(R.string.address_error_unparseable, rejection("https://").reason)
        assertEquals(R.string.address_error_no_host, rejection("/board").reason)
        assertEquals(R.string.address_error_https_only, rejection("ssh://my-server.ts.net").reason)
        assertEquals(R.string.address_error_port, rejection("my-server.ts.net:99999").reason)
    }

    @Test
    fun credentialsInTheAddressAreRefused() {
        assertEquals(
            R.string.address_error_credentials,
            rejection("https://me:secret@my-server.ts.net").reason,
        )
    }

    @Test
    fun theRejectedAddressIsCarriedAsAFormatArgument() {
        // The sentence lives in strings.xml; what varies travels beside it.
        assertArrayEquals(arrayOf("/board"), rejection("/board").detail)
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
