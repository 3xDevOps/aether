import { useLayoutEffect, useState } from 'react'
import type { Terminal } from '@xterm/xterm'
import { clampTerminalFontSize, maxTerminalFontSize, minFitTerminalFontSize, minTerminalFontSize } from '@/lib/term-font'
import { useStore } from '@/store'

/** Fitted sizes snap to this step, so a resize settles on one size instead of hunting. */
const fitStep = 0.25

/**
 * Draws a phone's terminal at the size that fits the shared PTY's columns
 * into the pane, and lets two fingers choose a size. A phone follows the
 * PTY rather than sizing it (see docs/dashboard-frontend.md), so the grid
 * is as wide as the widest writer made it and only the text size can bring
 * it inside the screen. Returns the size the terminal is drawn at.
 */
export function useTerminalFit(terminal: Terminal | null, enabled: boolean): number {
  const fitWidth = useStore((state) => state.terminalFitWidth)
  const chosen = useStore((state) => state.terminalFontSize)
  const [drawn, setDrawn] = useState(chosen)

  useLayoutEffect(() => {
    const host = terminal?.element?.parentElement
    if (!terminal || !host) return
    const apply = (size: number) => {
      if (terminal.options.fontSize !== size) terminal.options.fontSize = size
      setDrawn(size)
    }
    if (!enabled) {
      setDrawn(chosen)
      return
    }
    let widthFit = chosen
    let pinch: { distance: number; from: number; size: number } | null = null

    const room = () => {
      const style = getComputedStyle(host)
      return host.clientWidth - Number.parseFloat(style.paddingLeft) - Number.parseFloat(style.paddingRight)
    }
    const gridWidth = () =>
      terminal.element?.querySelector<HTMLElement>('.xterm-screen')?.getBoundingClientRect().width ?? 0
    // Grows only on a change of pane, grid or preference; a render only
    // corrects an overflow, so device-pixel rounding cannot make it oscillate.
    const fit = (grow: boolean) => {
      if (pinch) return
      const width = room()
      const current = terminal.options.fontSize ?? chosen
      const drawnWidth = gridWidth()
      if (width <= 0 || drawnWidth <= 0) return
      const proportional = Math.floor((current * width) / drawnWidth / fitStep) * fitStep
      if (!grow && proportional >= current) return
      widthFit = Math.max(minFitTerminalFontSize, proportional)
      if (fitWidth) apply(Math.min(chosen, widthFit))
      for (let step = 0; fitWidth && step < 8 && gridWidth() > width && (terminal.options.fontSize ?? 0) > minFitTerminalFontSize; step++) {
        widthFit = (terminal.options.fontSize ?? current) - fitStep
        apply(widthFit)
      }
    }
    if (!fitWidth) apply(chosen)
    fit(true)

    const distance = (touches: TouchList) =>
      Math.hypot(touches[0].clientX - touches[1].clientX, touches[0].clientY - touches[1].clientY)
    const start = (event: TouchEvent) => {
      if (event.touches.length !== 2) return
      const size = terminal.options.fontSize ?? chosen
      pinch = { distance: distance(event.touches), from: size, size }
    }
    const move = (event: TouchEvent) => {
      if (!pinch || event.touches.length !== 2) return
      event.preventDefault()
      event.stopPropagation()
      const scaled = (pinch.from * distance(event.touches)) / pinch.distance
      pinch.size = Math.min(maxTerminalFontSize, Math.max(minFitTerminalFontSize, Math.round(scaled * 2) / 2))
      apply(pinch.size)
    }
    const end = (event: TouchEvent) => {
      if (!pinch || event.touches.length >= 2) return
      const { size, from } = pinch
      pinch = null
      if (size === from) return
      // Pinching in to the fitted size hands sizing back to the fit; the
      // smallest chosen size is the zoom minimum, so a pinch between the two
      // fits too.
      if (size < minTerminalFontSize || size <= widthFit) {
        useStore.getState().fitTerminalWidth()
        apply(Math.min(chosen, widthFit))
      } else {
        apply(clampTerminalFontSize(size))
        useStore.getState().setTerminalFontSize(size)
      }
    }

    const observer = new ResizeObserver(() => fit(true))
    observer.observe(host)
    const resized = terminal.onResize(() => fit(true))
    const rendered = terminal.onRender(() => fit(false))
    host.addEventListener('touchstart', start, { capture: true, passive: true })
    host.addEventListener('touchmove', move, { capture: true, passive: false })
    host.addEventListener('touchend', end, { capture: true, passive: true })
    host.addEventListener('touchcancel', end, { capture: true, passive: true })
    return () => {
      // A disposed terminal has left the page and takes no options.
      if (terminal.element?.isConnected) terminal.options.fontSize = useStore.getState().terminalFontSize
      observer.disconnect()
      resized.dispose()
      rendered.dispose()
      host.removeEventListener('touchstart', start, true)
      host.removeEventListener('touchmove', move, true)
      host.removeEventListener('touchend', end, true)
      host.removeEventListener('touchcancel', end, true)
    }
  }, [chosen, enabled, fitWidth, terminal])

  return drawn
}
