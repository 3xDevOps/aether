package io.aether.android

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SafeAreaTest {
    @Test
    fun webViewsFromM140OnwardsReportSafeAreas() {
        assertTrue(forwardsSafeAreaInsets("140.0.7259.20"))
        assertTrue(forwardsSafeAreaInsets("144.0.0.0"))
    }

    @Test
    fun olderWebViewsDoNot() {
        // 133 is what an Android 16 emulator image ships; 136 to 139 is the
        // range the two published sources disagree about.
        assertFalse(forwardsSafeAreaInsets("133.0.6943.137"))
        assertFalse(forwardsSafeAreaInsets("136.0.7103.60"))
        assertFalse(forwardsSafeAreaInsets("139.0.0.0"))
    }

    @Test
    fun anUnreadableVersionPadsNatively() {
        assertFalse(forwardsSafeAreaInsets(null))
        assertFalse(forwardsSafeAreaInsets(""))
        assertFalse(forwardsSafeAreaInsets("dev"))
    }
}
