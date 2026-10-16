package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Test

class RendererLossTest {
    @Test
    fun onlyAReclaimedRendererIsReloaded() {
        assertEquals(RendererLoss.RELOAD, rendererLoss(didCrash = false, terminated = false))
        assertEquals(RendererLoss.CRASHED, rendererLoss(didCrash = true, terminated = false))
    }

    @Test
    fun aRendererTheShellEndedIsShownAsUnresponsive() {
        // terminate() reports as a kill, not a crash.
        assertEquals(RendererLoss.UNRESPONSIVE, rendererLoss(didCrash = false, terminated = true))
        assertEquals(RendererLoss.UNRESPONSIVE, rendererLoss(didCrash = true, terminated = true))
    }
}
