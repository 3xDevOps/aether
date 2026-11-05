package io.aether.android

/**
 * Whether the page at [origin] may record audio.
 *
 * Only the dashboard's own origin, and only the microphone: a request that
 * names any other origin, or no audio capture, gets nothing. [origin] is the
 * WebView's `PermissionRequest.origin` text.
 */
fun grantsAudioCapture(
    base: String?,
    origin: String,
    resources: Array<String>,
): Boolean = base != null && isDashboardUrl(base, origin) && AUDIO_CAPTURE in resources

const val AUDIO_CAPTURE = "android.webkit.resource.AUDIO_CAPTURE"
