package io.aether.android

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SafeAreaTest {
    @Test
    fun webViewsFromM136OnwardsReportSafeAreas() {
        assertTrue(forwardsSafeAreaInsets("136.0.7103.60"))
        assertTrue(forwardsSafeAreaInsets("144.0.0.0"))
    }

    @Test
    fun olderWebViewsDoNot() {
        // What an Android 16 emulator image ships.
        assertFalse(forwardsSafeAreaInsets("133.0.6943.137"))
        assertFalse(forwardsSafeAreaInsets("99.0.1.1"))
    }

    @Test
    fun anUnreadableVersionPadsNatively() {
        assertFalse(forwardsSafeAreaInsets(null))
        assertFalse(forwardsSafeAreaInsets(""))
        assertFalse(forwardsSafeAreaInsets("dev"))
    }
}
