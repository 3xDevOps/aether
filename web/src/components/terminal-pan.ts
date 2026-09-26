import { useLayoutEffect } from 'react'
import type { Terminal } from '@xterm/xterm'

export function useTerminalPan(terminal: Terminal | null, enabled: boolean) {
  useLayoutEffect(() => {
    const host = terminal?.element?.parentElement
    if (!enabled || !terminal || !host) return
    let touch: { x: number; y: number; moved: boolean; panned: boolean } | null = null
    let following = true
    let frame = 0

    const revealCursor = () => {
      frame = 0
      if (!following || host.inert) return
      const screen = terminal.element?.querySelector<HTMLElement>('.xterm-screen')
      if (!screen) return
      const bounds = screen.getBoundingClientRect()
      const viewport = host.getBoundingClientRect()
      // client sizes round fractional CSS dimensions; keep the entire cell inside.
      const height = Math.floor(viewport.height - host.offsetHeight + host.clientHeight)
      const buffer = terminal.buffer.active
      const cellHeight = bounds.height / terminal.rows
      const top = bounds.top - viewport.top + host.scrollTop +
        (buffer.baseY + buffer.cursorY - buffer.viewportY) * cellHeight
      if (top < host.scrollTop) host.scrollTop = Math.floor(top)
      else if (top + cellHeight > host.scrollTop + height) {
        host.scrollTop = Math.ceil(top + cellHeight - height)
      }
      if (terminal.textarea === document.activeElement) {
        const width = Math.floor(viewport.width - host.offsetWidth + host.clientWidth)
        const cellWidth = bounds.width / terminal.cols
        const left = bounds.left - viewport.left + host.scrollLeft + buffer.cursorX * cellWidth
        if (left < host.scrollLeft) host.scrollLeft = Math.floor(left)
        else if (left + cellWidth > host.scrollLeft + width) {
          host.scrollLeft = Math.ceil(left + cellWidth - width)
        }
      }
    }
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(revealCursor)
    }
    const resume = () => { following = true; schedule() }
    const start = (event: TouchEvent) => {
      const point = event.touches.length === 1 ? event.touches[0] : undefined
      touch = point ? { x: point.clientX, y: point.clientY, moved: false, panned: false } : null
    }
    const move = (event: TouchEvent) => {
      const point = event.touches[0]
      if (!touch || !point || event.touches.length !== 1 || event.defaultPrevented) return
      const dx = touch.x - point.clientX
      const dy = touch.y - point.clientY
      if (!touch.moved && Math.max(Math.abs(dx), Math.abs(dy)) < 6) return
      touch.moved = true
      touch.x = point.clientX
      touch.y = point.clientY
      following = false
      const horizontal = Math.abs(dx) > Math.abs(dy)
      if (!horizontal && host.scrollHeight <= host.clientHeight) return
      if (horizontal && (host.scrollWidth <= host.clientWidth ||
        (dx < 0 && host.scrollLeft <= 0) ||
        (dx > 0 && host.scrollLeft >= host.scrollWidth - host.clientWidth))) return
      // At the top of the shared screen, normal-buffer history owns the drag.
      if (!horizontal && dy < 0 && host.scrollTop <= 0 &&
        terminal.buffer.active === terminal.buffer.normal) return
      touch.panned = true
      event.preventDefault()
      event.stopPropagation()
      if (horizontal) host.scrollLeft += dx
      else host.scrollTop += dy
    }
    const end = (event: TouchEvent) => {
      if (touch?.panned) {
        event.preventDefault()
        event.stopPropagation()
      }
      touch = null
    }
    const wheel = (event: WheelEvent) => {
      if (event.ctrlKey || event.defaultPrevented) return
      following = false
      if (host.scrollHeight <= host.clientHeight || (event.deltaY < 0 && host.scrollTop <= 0)) return
      event.preventDefault()
      event.stopPropagation()
      const unit = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? host.clientHeight : 1
      host.scrollTop += event.deltaY * unit
      host.scrollLeft += event.deltaX * unit
    }
    const observer = new ResizeObserver(schedule)
    observer.observe(host)
    const render = terminal.onRender(schedule)
    const input = terminal.onData(resume)
    host.addEventListener('focusin', resume)
    host.addEventListener('touchstart', start, { capture: true, passive: true })
    host.addEventListener('touchmove', move, { capture: true, passive: false })
    host.addEventListener('touchend', end, { capture: true, passive: false })
    host.addEventListener('touchcancel', end, { capture: true, passive: false })
    host.addEventListener('wheel', wheel, { capture: true, passive: false })
    schedule()
    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
      render.dispose()
      input.dispose()
      host.removeEventListener('focusin', resume)
      host.removeEventListener('touchstart', start, true)
      host.removeEventListener('touchmove', move, true)
      host.removeEventListener('touchend', end, true)
      host.removeEventListener('touchcancel', end, true)
      host.removeEventListener('wheel', wheel, true)
    }
  }, [enabled, terminal])
}
