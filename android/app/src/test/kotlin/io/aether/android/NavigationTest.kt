package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

private const val BASE = "https://my-server.ts.net/"

class NavigationTest {
    private fun mainFrame(url: String, scheme: String?) =
        navigationFor(BASE, url, scheme, mainFrame = true)

    private fun refuses(url: String, scheme: String?) =
        refusesRequest(BASE, url, scheme, mainFrame = true)

    @Test
    fun aPopupGoesToTheBrowserOrNowhere() {
        assertEquals(Navigation.LOAD, popupNavigation("about:blank", "about"))
        assertEquals(Navigation.EXTERNAL, popupNavigation("https://github.com/login/device", "https"))
        assertEquals(Navigation.EXTERNAL, popupNavigation("http://example.com/", "http"))
        assertEquals(Navigation.EXTERNAL, popupNavigation("mailto:a@example.com", "mailto"))
        assertEquals(Navigation.EXTERNAL, popupNavigation("tel:+15551234", "tel"))
        assertEquals(Navigation.DROP, popupNavigation("about:srcdoc", "about"))
        assertEquals(Navigation.DROP, popupNavigation("data:text/html,x", "data"))
        assertEquals(Navigation.DROP, popupNavigation("blob:https://a/b", "blob"))
        assertEquals(Navigation.DROP, popupNavigation("intent://x#Intent;end", "intent"))
        assertEquals(Navigation.DROP, popupNavigation("javascript:alert(1)", "javascript"))
    }

    @Test
    fun theDashboardStaysInTheWebView() {
        assertEquals(Navigation.LOAD, mainFrame("https://my-server.ts.net/?run=01k4x7m2qc", "https"))
        assertEquals(Navigation.LOAD, mainFrame("https://MY-SERVER.ts.net/board", "https"))
    }

    @Test
    fun thePagesOwnSchemesStayInTheWebView() {
        // A download the page built itself, and an about:blank the WebView
        // loads while a navigation settles.
        assertEquals(Navigation.LOAD, mainFrame("blob:https://my-server.ts.net/a-diff", "blob"))
        assertEquals(Navigation.LOAD, mainFrame("data:text/plain,log", "data"))
        assertEquals(Navigation.LOAD, mainFrame("about:blank", "about"))
    }

    @Test
    fun ordinaryLinksElsewhereGoToTheBrowser() {
        // An agent's OAuth page and a link in a run summary.
        assertEquals(Navigation.EXTERNAL, mainFrame("https://github.com/login/oauth", "https"))
        assertEquals(Navigation.EXTERNAL, mainFrame("http://example.test/", "http"))
        assertEquals(Navigation.EXTERNAL, mainFrame("mailto:someone@example.test", "mailto"))
        // Another port on the same host is another origin.
        assertEquals(Navigation.EXTERNAL, mainFrame("https://my-server.ts.net:8443/", "https"))
    }

    @Test
    fun everyOtherSchemeIsDropped() {
        // intent:// starts another app with extras the page chose; the rest
        // would leave an ERR_UNKNOWN_URL_SCHEME page where the dashboard was.
        assertEquals(Navigation.DROP, mainFrame("intent://scan#Intent;scheme=zxing;end", "intent"))
        assertEquals(Navigation.DROP, mainFrame("market://details?id=io.aether.android", "market"))
        assertEquals(Navigation.DROP, mainFrame("javascript:alert(1)", "javascript"))
        assertEquals(Navigation.DROP, mainFrame("file:///sdcard/notes.txt", "file"))
        assertEquals(Navigation.DROP, mainFrame("content://media/external/images/1", "content"))
        assertEquals(Navigation.DROP, mainFrame("not a url", null))
    }

    @Test
    fun aSubframeNeverLeavesTheShell() {
        assertEquals(
            Navigation.DROP,
            navigationFor(BASE, "https://example.test/", "https", mainFrame = false),
        )
        assertEquals(
            Navigation.LOAD,
            navigationFor(BASE, "https://my-server.ts.net/board", "https", mainFrame = false),
        )
    }

    @Test
    fun withoutADashboardNothingIsOnItsOrigin() {
        assertEquals(
            Navigation.EXTERNAL,
            navigationFor(null, "https://my-server.ts.net/", "https", mainFrame = true),
        )
    }

    @Test
    fun anOffOriginMainFrameRequestIsRefusedBeforeItIsSent() {
        // WebView skips shouldOverrideUrlLoading for a POST, so this is the
        // gate a form submitted off-origin meets instead.
        assertTrue(refuses("https://phish.example/", "https"))
        assertTrue(refuses("http://phish.example/", "http"))
        // Another port on the same host is another origin.
        assertTrue(refuses("https://my-server.ts.net:8443/", "https"))
    }

    @Test
    fun aRequestThisGateCannotJudgeIsLeftToTheOthers() {
        // Only http and https reach the network, and the gate in
        // shouldOverrideUrlLoading has already dropped every other scheme.
        assertFalse(refuses("intent://scan#Intent;scheme=zxing;end", "intent"))
        assertFalse(refuses("not a url", null))
    }

    @Test
    fun theDashboardsOwnRequestsAreNeverRefused() {
        assertFalse(refuses("https://my-server.ts.net/api/v1/run.list", "https"))
        assertFalse(refuses("https://MY-SERVER.ts.net:443/board", "https"))
        // A subresource is the dashboard fetching its own files, and the
        // page's own schemes put nothing on the wire.
        assertFalse(
            refusesRequest(BASE, "https://fonts.example/x.woff2", "https", mainFrame = false),
        )
        assertFalse(refuses("blob:https://my-server.ts.net/a-diff", "blob"))
        assertFalse(refuses("data:text/plain,log", "data"))
    }

    @Test
    fun withoutADashboardEveryMainFrameRequestIsRefused() {
        assertTrue(refusesRequest(null, "https://my-server.ts.net/", "https", mainFrame = true))
    }

    @Test
    fun aLoadAlreadyUnderWayIsJudgedTheSameWay() {
        assertTrue(startedOnDashboard(BASE, "https://my-server.ts.net/board"))
        assertTrue(startedOnDashboard(BASE, "about:blank"))
        assertFalse(startedOnDashboard(BASE, "https://phish.example/"))
        assertFalse(startedOnDashboard(BASE, "https://my-server.ts.net:8443/"))
        assertFalse(startedOnDashboard(BASE, "not a url"))
    }

    @Test
    fun theLoopbackGatewayIsTheOnlyOriginInEdgeMode() {
        val gateway = "http://127.0.0.1:41233/?token=abc"
        assertEquals(
            Navigation.LOAD,
            navigationFor(gateway, "http://127.0.0.1:41233/?token=abc&run=01k4x7m2qc", "http", mainFrame = true),
        )
        assertFalse(refusesRequest(gateway, "http://127.0.0.1:41233/api/v1/run.list", "http", mainFrame = true))
        for (other in listOf(
            "http://127.0.0.1:41234/",
            "https://127.0.0.1:41233/",
            "http://localhost:41233/",
            "http://127.0.0.2:41233/",
            "http://evil.example:41233/",
        )) {
            assertFalse(other, isDashboardUrl(gateway, other))
            assertEquals(other, Navigation.EXTERNAL, navigationFor(gateway, other, other.substringBefore(':'), mainFrame = true))
            assertTrue(other, refusesRequest(gateway, other, other.substringBefore(':'), mainFrame = true))
        }
    }
}
