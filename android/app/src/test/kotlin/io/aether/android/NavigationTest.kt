package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Test

private const val BASE = "https://my-server.ts.net/"

class NavigationTest {
    private fun mainFrame(url: String, scheme: String?) =
        navigationFor(BASE, url, scheme, mainFrame = true)

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
    fun aLoadAlreadyUnderWayIsJudgedTheSameWay() {
        // WebView skips shouldOverrideUrlLoading for a POST, so this is the
        // gate a form submitted off-origin meets instead.
        assertEquals(Navigation.LOAD, startedNavigationFor(BASE, "https://my-server.ts.net/board"))
        assertEquals(Navigation.LOAD, startedNavigationFor(BASE, "about:blank"))
        assertEquals(Navigation.EXTERNAL, startedNavigationFor(BASE, "https://phish.example/"))
        assertEquals(Navigation.EXTERNAL, startedNavigationFor(BASE, "https://my-server.ts.net:8443/"))
        // No scheme to read is no origin to match.
        assertEquals(Navigation.DROP, startedNavigationFor(BASE, "not a url"))
    }
}
