package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ShareTest {
    @Test
    fun shareBecomesAPrefilledPrompt() {
        assertEquals("/?new=1&prompt=fix%20https%3A%2F%2Fx.test%2Fa%3Fb%3D1%26c", sharePath("  fix https://x.test/a?b=1&c \n"))
        assertNull(sharePath("   "))
        assertNull(sharePath(null))
        val long = "a".repeat(MAX_SHARED_CHARS - 1) + "😀"
        val cut = sharePath(long)!!
        // The emoji's pair would straddle the cut, so neither half is kept.
        assertEquals("/?new=1&prompt=" + "a".repeat(MAX_SHARED_CHARS - 1), cut)
        assertTrue(isDashboardPath(sharePath("line one\nline two")!!))
    }

    @Test
    fun backLeavesOnlyFromHome() {
        assertTrue(atDashboardHome("https://h.ts.net/"))
        assertTrue(atDashboardHome("http://127.0.0.1:4000/?token=abc"))
        assertTrue(atDashboardHome("http://127.0.0.1:4000/"))
        assertTrue(atDashboardHome("about:blank"))
        assertTrue(atDashboardHome(null))
        assertFalse(atDashboardHome("https://h.ts.net/?run=r"))
        assertFalse(atDashboardHome("http://127.0.0.1:4000/?token=abc&page=workspace"))
    }
}
