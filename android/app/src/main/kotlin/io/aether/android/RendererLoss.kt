package io.aether.android

/** What the shell does once the dashboard's renderer is gone. */
enum class RendererLoss {
    /** Android reclaimed it: load the dashboard again once on screen. */
    RELOAD,

    /** It crashed. Show that, with Retry: reloading would crash it again. */
    CRASHED,

    /** The shell ended it for not responding. Show that, with Retry. */
    UNRESPONSIVE,
}

/**
 * [didCrash] is `RenderProcessGoneDetail.didCrash`, which is false for a
 * renderer ended with `terminate`, so [terminated] says whether the shell
 * ended it itself. Only a renderer Android reclaimed is reloaded unasked: a
 * crash or a hang would come back on the same page.
 */
fun rendererLoss(didCrash: Boolean, terminated: Boolean): RendererLoss =
    when {
        terminated -> RendererLoss.UNRESPONSIVE
        didCrash -> RendererLoss.CRASHED
        else -> RendererLoss.RELOAD
    }
