package io.aether.android

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AudioCaptureTest {
    private val base = "https://my-server.tailnet-name.ts.net/"
    private val mic = arrayOf(AUDIO_CAPTURE)

    @Test
    fun dashboardOriginMayRecord() {
        assertTrue(grantsAudioCapture(base, "https://my-server.tailnet-name.ts.net/", mic))
        assertTrue(grantsAudioCapture(base, "https://my-server.tailnet-name.ts.net:443/", mic))
    }

    @Test
    fun otherOriginsMayNot() {
        assertFalse(grantsAudioCapture(base, "https://evil.example/", mic))
        assertFalse(grantsAudioCapture(base, "http://my-server.tailnet-name.ts.net/", mic))
        assertFalse(grantsAudioCapture(base, "https://my-server.tailnet-name.ts.net:8443/", mic))
        assertFalse(grantsAudioCapture(base, "", mic))
    }

    @Test
    fun noDashboardLoadedMeansNoRecording() {
        assertFalse(grantsAudioCapture(null, "https://my-server.tailnet-name.ts.net/", mic))
    }

    @Test
    fun onlyTheMicrophoneIsEverGranted() {
        val camera = "android.webkit.resource.VIDEO_CAPTURE"
        assertFalse(grantsAudioCapture(base, base, arrayOf(camera)))
        assertFalse(grantsAudioCapture(base, base, emptyArray()))
        assertTrue(grantsAudioCapture(base, base, arrayOf(camera, AUDIO_CAPTURE)))
    }
}
