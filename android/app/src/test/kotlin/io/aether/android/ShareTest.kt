package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ShareTest {
    @Test
    fun subjectComesFirstUnlessTheTextHoldsIt() {
        assertEquals("Flaky build\nhttps://x.test/1", sharedText(" https://x.test/1 ", "Flaky build"))
        assertEquals("Flaky build: https://x.test/1", sharedText("Flaky build: https://x.test/1", "Flaky build"))
        assertEquals("Flaky build", sharedText(null, "Flaky build"))
        assertEquals("https://x.test/1", sharedText("https://x.test/1", " "))
        assertNull(sharedText(" ", null))
    }

    @Test
    fun shareBecomesAPrefilledPrompt() {
        assertEquals("/?new=1&prompt=fix%20https%3A%2F%2Fx.test%2Fa%3Fb%3D1%26c", sharePath("  fix https://x.test/a?b=1&c \n"))
        assertNull(sharePath("   "))
        assertNull(sharePath(null))
        // The cap counts code points, as the dashboard does: an emoji is one.
        assertEquals("/?new=1&prompt=" + "%F0%9F%98%80".repeat(MAX_SHARED_CHARS), sharePath("😀".repeat(MAX_SHARED_CHARS + 1)))
        assertEquals("/?new=1&prompt=" + "a".repeat(MAX_SHARED_CHARS), sharePath("a".repeat(MAX_SHARED_CHARS + 1)))
        // The URL MainActivity loads, on either dashboard.
        assertEquals("https://h.ts.net/?new=1&prompt=line%20one%0Aline%20two", dashboardAt("https://h.ts.net/", sharePath("line one\nline two")!!))
        assertEquals("&new=1&prompt=a", edgeSuffix(sharePath("a")!!))
    }

    @Test
    fun backLeavesOnlyFromHome() {
        assertTrue(atDashboardHome("https://h.ts.net/"))
        assertTrue(atDashboardHome("http://127.0.0.1:4000/?token=abc"))
        assertTrue(atDashboardHome("http://127.0.0.1:4000/"))
        // Where the dashboard sends the root before a server is linked.
        assertTrue(atDashboardHome("http://127.0.0.1:4000/?page=onboarding"))
        assertTrue(atDashboardHome("about:blank"))
        assertTrue(atDashboardHome(null))
        assertFalse(atDashboardHome("https://h.ts.net/?run=r"))
        assertFalse(atDashboardHome("http://127.0.0.1:4000/?token=abc&page=workspace"))
    }
}
