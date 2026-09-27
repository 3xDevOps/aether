package io.aether.android

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// A server id is 26 lowercase base32 characters; the alphabet happens to be one.
private const val EDGE_HOST = "abcdefghijklmnopqrstuvwxyz.servers.example.test"
private const val EDGE = "https://$EDGE_HOST/"
private const val TAILNET = "https://my-server.ts.net/"
private val CODE = "c".repeat(43)
private const val STATE = "fake-state_0.9~"
private val LINK = "aether://auth/callback?code=$CODE&state=$STATE"

class SignInTest {
    private fun refusal(base: String, link: String): SignInLinkError =
        runCatching { signInReturnUrl(base, link) }.exceptionOrNull() as SignInLinkError

    @Test
    fun theDashboardsSignInAsksForTheAppReturn() {
        assertEquals("${EDGE}auth/login?return=app", appSignInUrl(EDGE, "${EDGE}auth/login"))
        assertEquals(
            "${EDGE}auth/login?next=%2Fboard&return=app",
            appSignInUrl(EDGE, "${EDGE}auth/login?next=%2Fboard#top"),
        )
        // The origin is the saved one, however the page spelled it.
        assertEquals(
            "${EDGE}auth/login?return=app",
            appSignInUrl(EDGE, "https://${EDGE_HOST.uppercase()}:443/auth/login"),
        )
    }

    @Test
    fun aSignInThatAlreadyAsksForTheAppIsLeftAlone() {
        assertNull(appSignInUrl(EDGE, "${EDGE}auth/login?return=app"))
        assertNull(appSignInUrl(EDGE, "${EDGE}auth/login?next=%2F&return=app"))
    }

    @Test
    fun everyOtherNavigationIsNotTheSignIn() {
        assertNull(appSignInUrl(EDGE, EDGE))
        assertNull(appSignInUrl(EDGE, "${EDGE}auth/login/"))
        assertNull(appSignInUrl(EDGE, "${EDGE}auth/callback?code=$CODE&state=$STATE"))
        assertNull(appSignInUrl(EDGE, "https://phish.example/auth/login"))
        assertNull(appSignInUrl(EDGE, "https://$EDGE_HOST:8443/auth/login"))
        assertNull(appSignInUrl(null, "${EDGE}auth/login"))
    }

    @Test
    fun theEdgesSignInPageGoesToTheBrowser() {
        // What the server redirects /auth/login?return=app to.
        val authorize =
            "https://edge.example.test/authorize?server=abcdefghijklmnopqrstuvwxyz" +
                "&state=$STATE&challenge=$CODE&return=app"
        assertEquals(Navigation.EXTERNAL, navigationFor(EDGE, authorize, "https", mainFrame = true))
    }

    @Test
    fun theReturnLinkCompletesTheSignInOnTheSavedDashboard() {
        val want = "${EDGE}auth/callback?code=$CODE&state=$STATE"
        assertEquals(want, signInReturnUrl(EDGE, LINK))
        assertEquals(want, signInReturnUrl(EDGE, "aether://auth/callback?state=$STATE&code=$CODE"))
        assertEquals(want, signInReturnUrl(EDGE, "AETHER://auth/callback?code=$CODE&state=$STATE"))
    }

    @Test
    fun theReturnLinkIsRecognisedWhateverItsShape() {
        assertTrue(isSignInLink(LINK))
        assertTrue(isSignInLink("aether://auth"))
        assertTrue(isSignInLink("AETHER://auth?code=x"))
        assertFalse(isSignInLink("aether://run/01k4x7m2qc"))
        assertFalse(isSignInLink("aether://authority/callback"))
        assertFalse(isSignInLink("https://auth/callback"))
    }

    @Test
    fun aLinkThatIsNotExactlyTheEdgesReturnIsRefused() {
        val query = "?code=$CODE&state=$STATE"
        val links =
            listOf(
                "https://$EDGE_HOST/auth/callback$query",
                "aether://phish.example/callback$query",
                "aether://AUTH/callback$query",
                "aether://auth/callback/$query",
                "aether://auth/%63allback$query",
                "aether://auth/other$query",
                "aether://someone@auth/callback$query",
                "aether://auth:443/callback$query",
                "aether://auth/callback$query#fragment",
                "aether://auth/callback",
                "aether://auth/callback?",
                "aether://auth/callback?code=$CODE",
                "aether://auth/callback?code=$CODE&code=$CODE",
                "aether://auth/callback?code=$CODE&state",
                "aether://auth/callback?code=$CODE&state=$STATE&",
                "aether://auth/callback?code=$CODE&state=$STATE&next=https://phish.example/",
                "aether://auth/callback?code=$CODE&state=$STATE&host=phish.example",
                // The link names no server; one that tries to is not the edge's.
                "aether://auth/callback?code=$CODE&state=$STATE&server=bbbbbbbbbbbbbbbbbbbbbbbbbb",
                "aether://auth/callback?code=$CODE&state=${"s".repeat(600)}",
                "aether://auth/callback?code=$CODE&state=$STATE with space",
            )
        for (link in links) {
            assertEquals(link, R.string.sign_in_link_error_shape, refusal(EDGE, link).reason)
        }
    }

    @Test
    fun aCodeOrStateTheEdgeDoesNotIssueIsRefused() {
        val links =
            listOf(
                "aether://auth/callback?code=${"c".repeat(42)}&state=$STATE",
                "aether://auth/callback?code=${"c".repeat(44)}&state=$STATE",
                "aether://auth/callback?code=${"c".repeat(42)}+&state=$STATE",
                "aether://auth/callback?code=${"c".repeat(40)}%2F&state=$STATE",
                "aether://auth/callback?code=$CODE&state=",
                "aether://auth/callback?code=$CODE&state=${"s".repeat(257)}",
                "aether://auth/callback?code=$CODE&state=a%26next%3Dx",
                "aether://auth/callback?code=$CODE&state=a/b",
            )
        for (link in links) {
            assertEquals(link, R.string.sign_in_link_error_values, refusal(EDGE, link).reason)
        }
    }

    @Test
    fun onlyADashboardReachedThroughAnEdgeTakesTheReturn() {
        // A tailnet dashboard has no edge sign-in, so a code is never sent there.
        val refused = refusal(TAILNET, LINK)
        assertEquals(R.string.sign_in_link_error_not_edge, refused.reason)
        assertArrayEquals(arrayOf("my-server.ts.net"), refused.detail)
        assertEquals(
            R.string.sign_in_link_error_not_edge,
            refusal("https://abcdefghijklmnopqrstuvwxyz/", LINK).reason,
        )
        assertEquals(
            R.string.sign_in_link_error_not_edge,
            refusal("https://abcdefghijklmnopqrstuvwxy1.servers.example.test/", LINK).reason,
        )
        // dashboardUrl keeps the host as typed.
        assertEquals(
            "https://${EDGE_HOST.uppercase()}/auth/callback?code=$CODE&state=$STATE",
            signInReturnUrl("https://${EDGE_HOST.uppercase()}/", LINK),
        )
    }
}
