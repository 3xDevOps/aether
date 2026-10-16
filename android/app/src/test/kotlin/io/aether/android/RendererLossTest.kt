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
}
