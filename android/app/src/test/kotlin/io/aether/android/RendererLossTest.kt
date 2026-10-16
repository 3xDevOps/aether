package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class RendererLossTest {
    @Test
    fun onlyARendererReclaimedInTheBackgroundIsReloaded() {
        assertEquals(RendererLoss.RELOAD, rendererLoss(didCrash = false, terminated = false, started = false))
        assertEquals(RendererLoss.KILLED, rendererLoss(didCrash = false, terminated = false, started = true))
        assertEquals(RendererLoss.CRASHED, rendererLoss(didCrash = true, terminated = false, started = false))
        assertEquals(RendererLoss.CRASHED, rendererLoss(didCrash = true, terminated = false, started = true))
    }

    @Test
    fun aRendererTheShellEndedIsShownAsUnresponsive() {
        // terminate() reports as a kill, not a crash.
        assertEquals(RendererLoss.UNRESPONSIVE, rendererLoss(didCrash = false, terminated = true, started = true))
        assertEquals(RendererLoss.UNRESPONSIVE, rendererLoss(didCrash = true, terminated = true, started = false))
    }

    @Test
    fun oneLongTaskIsLetFinish() {
        assertFalse(endsUnresponsiveRenderer(1))
        assertTrue(endsUnresponsiveRenderer(2))
        assertTrue(endsUnresponsiveRenderer(3))
    }

    @Test
    fun aReclaimedDashboardReopensTheRouteThatWasOpen() {
        val edge = "http://127.0.0.1:41233/?token=aB3_-xY9"
        assertEquals(
            "$edge&page=run&run=run-7k2m9q4xbd",
            recoveryUrl(edge, "http://127.0.0.1:41233/?page=run&run=run-7k2m9q4xbd#x"),
        )
        val tailnet = "https://my-server.tailnet-name.ts.net/"
        assertEquals("$tailnet?run=run-7k2m9q4xbd", recoveryUrl(tailnet, "${tailnet}?run=run-7k2m9q4xbd"))
    }

    @Test
    fun aPageOffTheDashboardOrWithoutARouteReloadsTheBase() {
        val edge = "http://127.0.0.1:41233/?token=aB3_-xY9"
        assertEquals(edge, recoveryUrl(edge, ""))
        assertEquals(edge, recoveryUrl(edge, "about:blank"))
        assertEquals(edge, recoveryUrl(edge, "http://127.0.0.1:41234/?page=run"))
        assertEquals(edge, recoveryUrl(edge, "http://127.0.0.1:41233/"))
        // A token on the open page is not carried over a second time.
        assertEquals(edge, recoveryUrl(edge, "http://127.0.0.1:41233/?token=other"))
    }
}
