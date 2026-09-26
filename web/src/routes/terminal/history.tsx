import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { RefObject } from 'react'
import type { Terminal } from '@xterm/xterm'
import type { XtermController } from '@/components/xterm-host'
import type { TerminalReadSurface } from '@/components/terminal-pane'
import { captureTerminalPresentation, type FrozenTerminal } from '@/components/terminal-presentation'
import { Button } from '@/components/ui/button'
import { copyTerminalText } from '@/lib/term-clipboard'
import { defaultTerminalFontSize, terminalZoomKey } from '@/lib/term-font'
import type { TerminalHistoryLine } from '@/lib/types'
import { useStore } from '@/store'
import type { HistoryAnchor, HistoryCache, SavedHistoryView } from './history-cache'

const padding = 8
const overscan = 12
const maxScrollPixels = 4_000_000

type Row = { index: number; line: TerminalHistoryLine }
type Viewport = { top: number; left: number; height: number; width: number }
type CaptureIntent = { viewportY: number; top: number; left: number; delta: number }
type PendingCapture = { terminal: Terminal; buffer: Terminal['buffer']['active']; intent: CaptureIntent; dispose: () => void }

type TerminalHistoryProps = {
  controller: XtermController
  cache: HistoryCache
  enabled: boolean
  beforeDispose: RefObject<((terminal: Terminal) => void) | null>
  tools: RefObject<TerminalReadSurface | null>
  onReadingChange: (reading: boolean) => void
}

function lineTop(line: number, origin: number, height: number): number {
  // Scroll positions are pixel-snapped by the browser. Snap both edges of
  // every row too, so a fractional zoom cannot round the anchor differently.
  return padding + Math.round((line - origin) * height)
}

function rowTop(row: number, origin: number, height: number): number {
  return lineTop(row + (row >= 0 ? 1 : 0), origin, height)
}

function anchorAt(top: number, left: number, origin: number, height: number): HistoryAnchor {
  let line = origin + Math.floor((top - padding) / height)
  if (lineTop(line + 1, origin, height) <= top) line++
  else if (lineTop(line, origin, height) > top) line--
  line = Math.max(origin, line)
  const row = line < 0 ? line : Math.max(0, line - 1)
  return { row, offset: top - rowTop(row, origin, height), left }
}

function rowText(html: string): string {
  const element = document.createElement('div')
  element.innerHTML = html
  return element.textContent ?? ''
}

export function TerminalHistory({
  controller,
  cache,
  enabled,
  beforeDispose,
  tools,
  onReadingChange,
}: TerminalHistoryProps) {
  const [snapshot, setSnapshot] = useState(() => cache.snapshot())
  const [frozen, setFrozen] = useState<FrozenTerminal | null>(null)
  const [restoring, setRestoring] = useState(true)
  const [restoreError, setRestoreError] = useState<string | null>(null)
  const [restoreAttempt, setRestoreAttempt] = useState(0)
  const [episodeReady, setEpisodeReady] = useState(false)
  const [rows, setRows] = useState<Row[]>([])
  const [readError, setReadError] = useState<string | null>(null)
  const [readAttempt, setReadAttempt] = useState(0)
  const [match, setMatch] = useState<number | null>(null)
  const [viewport, setViewport] = useState<Viewport>({ top: 0, left: 0, height: 0, width: 0 })
  const [windowStart, setWindowStart] = useState<number | null>(null)
  const fontSize = useStore((state) => state.terminalFontSize)
  const scroller = useRef<HTMLDivElement>(null)
  const anchor = useRef<HistoryAnchor>({ row: 0, offset: 0, left: 0 })
  const lastTop = useRef(0)
  // Detached hosts lose their scroll offsets before xterm's effect cleanup.
  const livePan = useRef({ top: 0, left: 0, paddingTop: 0 })
  const maxColumns = useRef(0)
  const current = useRef<SavedHistoryView | null>(null)
  const previousScreen = useRef<FrozenTerminal | null>(null)
  const mounted = useRef(true)
  const searchRevision = useRef(0)
  const searchMatch = useRef<{ query: string; row: number } | null>(null)
  const touchY = useRef<number | null>(null)
  const touchX = useRef<number | null>(null)
  const surfaceTouchY = useRef<number | null>(null)
  const episode = useRef(0)
  const liveFocus = useRef<{ owner: Element | null; terminal: Terminal | null } | null>(null)
  const scrollbarPointer = useRef<number | null>(null)
  const pendingCapture = useRef<PendingCapture | null>(null)
  const terminal = controller.terminal
  const scale = frozen ? fontSize / frozen.fontSize : 1
  const cellHeight = frozen ? frozen.cellHeight * scale : 1
  const cellWidth = frozen ? frozen.cellWidth * scale : 1
  const count = episodeReady ? snapshot.count : 0
  const windowRows = Math.max(1, Math.floor(maxScrollPixels / cellHeight))
  const contentEnd = 1 + (frozen?.rows.length ?? 0)
  const anchorLine = anchor.current.row + (anchor.current.row >= 0 ? 1 : 0)
  const centeredOrigin = anchorLine - Math.floor(windowRows / 2)
  const requestedOrigin = windowStart ?? centeredOrigin
  const outsideWindow = anchorLine < requestedOrigin ||
    anchorLine >= requestedOrigin + windowRows - Math.ceil(viewport.height / cellHeight)
  const origin = Math.max(-count, Math.min(outsideWindow ? centeredOrigin : requestedOrigin, contentEnd - windowRows))
  const windowEnd = Math.min(contentEnd, origin + windowRows)
  const layout = useRef({ count, origin, cellHeight, contentEnd, windowRows })
  layout.current = { count, origin, cellHeight, contentEnd, windowRows }

  const save = () => {
    const view = current.current
    if (view) void cache.saveView({ screen: view.screen, anchor: { ...anchor.current } })
  }
  beforeDispose.current = (leaving) => {
    const pending = pendingCapture.current
    pendingCapture.current = null
    pending?.dispose()
    if (current.current) {
      save()
    } else if (!restoring && ((pending?.terminal === leaving && pending.buffer === leaving.buffer.active) ||
      (leaving.buffer.active === leaving.buffer.normal && leaving.buffer.normal.viewportY < leaving.buffer.normal.baseY))) {
      // Teardown cannot await another paint. Capture once from the still-live
      // terminal and retain the scroll intent if it beat the requested frame.
      const view = captureView(leaving, pending?.terminal === leaving ? pending.intent : {
        viewportY: leaving.buffer.normal.viewportY,
        top: livePan.current.top,
        left: livePan.current.left,
        delta: 0,
      })
      if (view) void cache.saveView(view)
    }
  }
  if (frozen) {
    current.current = {
      screen: {
        ...frozen,
        cellHeight,
        cellWidth,
        fontSize,
        letterSpacing: frozen.letterSpacing * scale,
      },
      anchor: anchor.current,
    }
  }

  useEffect(() => {
    mounted.current = true
    const unsubscribe = cache.subscribe(() => setSnapshot(cache.snapshot()))
    return () => {
      mounted.current = false
      liveFocus.current = null
      episode.current++
      searchRevision.current++
      unsubscribe()
      cache.cancel()
    }
  }, [cache])

  useEffect(() => {
    let active = true
    void cache.readView().then((saved) => {
      if (!active) return
      if (saved) {
        anchor.current = saved.anchor
        previousScreen.current = saved.screen
        maxColumns.current = saved.screen.cols
        current.current = saved
        setFrozen(saved.screen)
        setEpisodeReady(true)
      }
      setSnapshot(cache.snapshot())
      setRestoreError(null)
      setRestoring(false)
    }).catch((error: unknown) => {
      if (active) setRestoreError(String(error))
    })
    return () => { active = false }
  }, [cache, restoreAttempt])

  useLayoutEffect(() => {
    pendingCapture.current = null
    // Keep the cheap intent until beforeDispose has had a chance to save it;
    // disposing the listener prevents any late callback from entering history.
    return () => { pendingCapture.current?.dispose() }
  }, [cache, enabled, frozen, restoring, terminal])

  useEffect(() => {
    const intent = liveFocus.current
    if (!intent || restoring || frozen) return
    const cancel = () => { liveFocus.current = null }
    document.addEventListener('focusin', cancel)
    document.addEventListener('pointerdown', cancel, true)
    const frame = requestAnimationFrame(() => {
      const host = terminal?.element?.parentElement
      const active = document.activeElement
      if (liveFocus.current === intent && enabled && terminal === intent.terminal &&
        host?.isConnected && !host.inert && host.style.visibility !== 'hidden' &&
        (active === intent.owner || (active === document.body && !intent.owner?.isConnected))) {
        controller.focusTerminal()
      }
      liveFocus.current = null
    })
    return () => {
      cancelAnimationFrame(frame)
      document.removeEventListener('focusin', cancel)
      document.removeEventListener('pointerdown', cancel, true)
    }
  }, [controller.focusTerminal, enabled, frozen, restoring, terminal])

  const returnLive = () => {
    liveFocus.current = { owner: document.activeElement, terminal }
    current.current = null
    episode.current++
    touchY.current = null
    setWindowStart(null)
    setEpisodeReady(false)
    searchRevision.current++
    searchMatch.current = null
    setMatch(null)
    setFrozen(null)
    setRows([])
    setReadError(null)
    setReadAttempt(0)
    void cache.saveView(null)
    terminal?.scrollToBottom()
    controller.noteViewportInteraction?.()
  }

  const startArchive = () => {
    const generation = ++episode.current
    void cache.resetArchive().then(() => {
      if (mounted.current && episode.current === generation) {
        setReadError(null)
        setEpisodeReady(true)
      }
    }).catch((error: unknown) => {
      if (mounted.current && episode.current === generation) setReadError(String(error))
    })
  }

  const captureView = (source: Terminal, intent: CaptureIntent): SavedHistoryView | null => {
    const alternate = source.buffer.active === source.buffer.alternate
    let screen = captureTerminalPresentation(source)
    if (!screen.rows.length && previousScreen.current) screen = previousScreen.current
    if (!screen.rows.length && !alternate) return null
    if (!screen.rows.length) {
      const rendered = source.element?.querySelector<HTMLElement>('.xterm-screen')
      const bounds = rendered?.getBoundingClientRect()
      const height = Number.parseFloat(rendered?.style.height ?? '') || bounds?.height || 0
      const width = Number.parseFloat(rendered?.style.width ?? '') || bounds?.width || 0
      if (!height || !width) return null
      screen = { ...screen, cellHeight: height / source.rows, cellWidth: width / source.cols }
    }
    const viewportY = alternate ? screen.viewportY : intent.viewportY
    const top = rowTop(viewportY, -count, screen.cellHeight)
    return {
      screen,
      anchor: anchorAt(Math.max(0, top + intent.top + intent.delta), intent.left, -count, screen.cellHeight),
    }
  }

  const enter = (delta = 0, explicit = false) => {
    if (!terminal || !enabled || restoring || current.current) return
    if (terminal.buffer.active === terminal.buffer.alternate && !explicit) return
    const intent: CaptureIntent = {
      viewportY: terminal.buffer.normal.viewportY,
      top: Math.max(0, (terminal.element?.parentElement?.scrollTop ?? 0) - livePan.current.paddingTop),
      left: terminal.element?.parentElement?.scrollLeft ?? 0,
      delta,
    }
    const pending = pendingCapture.current
    if (pending?.terminal === terminal && pending.buffer === terminal.buffer.active) {
      intent.delta += pending.intent.delta
      pending.intent = intent
      return
    }
    pending?.dispose()
    const request: PendingCapture = {
      terminal,
      buffer: terminal.buffer.active,
      intent,
      dispose: () => subscription.dispose(),
    }
    const subscription = terminal.onRender(({ start, end }) => {
      if (start > 0 || end < terminal.rows - 1) return
      request.dispose()
      if (pendingCapture.current !== request) return
      pendingCapture.current = null
      if (!mounted.current || current.current || terminal.buffer.active !== request.buffer) return
      const view = captureView(terminal, request.intent)
      if (!view) return
      const { screen } = view
      previousScreen.current = screen
      anchor.current = view.anchor
      maxColumns.current = screen.cols
      current.current = view
      setFrozen(screen)
      terminal.blur()
      startArchive()
      controller.noteViewportInteraction?.()
    })
    pendingCapture.current = request
    // write() completion precedes DOM rendering. A full public refresh gives
    // capture matching cell content and rendered extended-decoration metadata.
    terminal.refresh(0, terminal.rows - 1)
  }

  useLayoutEffect(() => {
    const host = terminal?.element?.parentElement
    if (!terminal || !host || !enabled || restoring || frozen) return
    const normal = () => terminal.buffer.active === terminal.buffer.normal
    livePan.current.paddingTop = Number.parseFloat(getComputedStyle(host).paddingTop) || 0
    const rememberPan = () => {
      livePan.current.top = Math.max(0, host.scrollTop - livePan.current.paddingTop)
      livePan.current.left = host.scrollLeft
    }
    rememberPan()
    const wheel = (event: WheelEvent) => {
      if (!normal() || event.defaultPrevented || host.scrollTop > 0 || event.deltaY >= 0 || event.ctrlKey) return
      event.preventDefault()
      event.stopPropagation()
      const height = terminal.element?.querySelector('.xterm-screen')?.getBoundingClientRect().height
      const line = height ? height / terminal.rows : fontSize * 1.2
      enter(event.deltaY * (event.deltaMode === 1 ? line : event.deltaMode === 2 ? host.clientHeight : 1))
    }
    const key = (event: KeyboardEvent) => {
      const pageUp = event.code === 'PageUp' || event.key === 'PageUp'
      const home = event.code === 'Home' || event.key === 'Home'
      if ((!pageUp && !home) || event.ctrlKey || event.metaKey || event.altKey) return
      if (!normal() && !(pageUp && event.shiftKey)) return
      event.preventDefault()
      event.stopPropagation()
      enter(home ? -Number.MAX_SAFE_INTEGER : -host.clientHeight * 0.9, event.shiftKey)
    }
    const touchStart = (event: TouchEvent) => {
      touchY.current = event.touches[0]?.clientY ?? null
      touchX.current = event.touches[0]?.clientX ?? null
    }
    const touchMove = (event: TouchEvent) => {
      const point = event.touches[0]
      if (!point || touchY.current === null || touchX.current === null || event.touches.length !== 1) return
      const delta = touchY.current - point.clientY
      const horizontal = Math.abs(touchX.current - point.clientX) > Math.abs(delta)
      if (event.defaultPrevented || host.scrollTop > 0 || horizontal) {
        touchY.current = point.clientY
        touchX.current = point.clientX
        return
      }
      if (!normal() || delta > -6) return
      event.preventDefault()
      event.stopPropagation()
      enter(delta)
      touchY.current = point.clientY
    }
    const pointerDown = (event: PointerEvent) => {
      if (event.target instanceof Element &&
        event.target.closest('.xterm-scrollable-element > .scrollbar')) {
        rememberPan()
        scrollbarPointer.current = event.pointerId
      }
    }
    const pointerEnd = (event: PointerEvent) => {
      if (scrollbarPointer.current !== event.pointerId) return
      scrollbarPointer.current = null
      if (normal() && terminal.buffer.normal.viewportY < terminal.buffer.normal.baseY) enter()
    }
    const nativeScroll = terminal.onScroll(() => {
      // Keep xterm's drag owner visible through pointerup; freezing on its
      // first scroll would strand pointer capture on a hidden scrollbar.
      if (scrollbarPointer.current === null && normal() &&
        terminal.buffer.normal.viewportY < terminal.buffer.normal.baseY) enter()
    })
    host.addEventListener('pointerdown', pointerDown, true)
    host.addEventListener('scroll', rememberPan, { passive: true })
    document.addEventListener('pointerup', pointerEnd)
    document.addEventListener('pointercancel', pointerEnd)
    host.addEventListener('wheel', wheel, { capture: true, passive: false })
    host.addEventListener('keydown', key, true)
    host.addEventListener('touchstart', touchStart, { passive: true })
    host.addEventListener('touchmove', touchMove, { capture: true, passive: false })
    return () => {
      nativeScroll.dispose()
      host.removeEventListener('scroll', rememberPan)
      host.removeEventListener('pointerdown', pointerDown, true)
      document.removeEventListener('pointerup', pointerEnd)
      document.removeEventListener('pointercancel', pointerEnd)
      host.removeEventListener('wheel', wheel, true)
      host.removeEventListener('keydown', key, true)
      host.removeEventListener('touchstart', touchStart)
      host.removeEventListener('touchmove', touchMove, true)
    }
  })

  useLayoutEffect(() => {
    onReadingChange(restoring || frozen !== null)
  }, [frozen, restoring, onReadingChange])

  useEffect(() => {
    const move = (event: TouchEvent) => {
      const element = scroller.current
      const y = event.touches[0]?.clientY
      if (!element || touchY.current === null || y === undefined) return
      event.preventDefault()
      element.scrollTop += touchY.current - y
      touchY.current = y
    }
    const end = () => { touchY.current = null }
    document.addEventListener('touchmove', move, { passive: false })
    document.addEventListener('touchend', end)
    document.addEventListener('touchcancel', end)
    return () => {
      document.removeEventListener('touchmove', move)
      document.removeEventListener('touchend', end)
      document.removeEventListener('touchcancel', end)
    }
  }, [])

  useLayoutEffect(() => {
    const element = scroller.current
    if (!element || !frozen) return
    const measure = () => setViewport((old) => ({
      ...old,
      height: element.clientHeight,
      width: element.clientWidth,
    }))
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    element.focus({ preventScroll: true })
    return () => observer.disconnect()
  }, [frozen])

  useLayoutEffect(() => {
    const element = scroller.current
    if (!element || !frozen) return
    if (windowStart !== origin) setWindowStart(origin)
    const top = Math.max(0, rowTop(anchor.current.row, origin, cellHeight) + anchor.current.offset)
    element.scrollTop = top
    element.scrollLeft = anchor.current.left
    lastTop.current = element.scrollTop
    setViewport((old) => ({ ...old, top: element.scrollTop, left: element.scrollLeft }))
  }, [frozen, origin, cellHeight, windowStart, viewport.height, viewport.width])

  const startLine = Math.max(origin, origin + Math.floor(viewport.top / cellHeight) - overscan)
  const endLine = Math.min(windowEnd,
    origin + Math.ceil((viewport.top + viewport.height) / cellHeight) + overscan)
  const archiveStart = Math.min(0, startLine)
  const archiveEnd = Math.min(0, endLine)

  useEffect(() => {
    if (!frozen || !episodeReady || (archiveStart === archiveEnd && readAttempt === 0)) return
    let active = true
    void cache.readRows(archiveStart, archiveEnd).then((loaded) => {
      if (!active) return
      for (const row of loaded) maxColumns.current = Math.max(maxColumns.current, row.line.text.length)
      setReadError(null)
      setRows(loaded)
    }).catch((error: unknown) => { if (active) setReadError(String(error)) })
    return () => { active = false }
  }, [cache, frozen, episodeReady, archiveStart, archiveEnd, snapshot.revision, readAttempt])

  useEffect(() => {
    if (!frozen || !episodeReady || !snapshot.hasMore || snapshot.loading || snapshot.error || readError) return
    if (viewport.top + (origin + count) * cellHeight > Math.max(viewport.height * 3, cellHeight * 40)) return
    // Even a scan with no rows yields to input/paint before requesting its continuation.
    const timer = setTimeout(() => { void cache.loadOlder() }, 0)
    return () => clearTimeout(timer)
  }, [cache, frozen, episodeReady, snapshot, readError, viewport.top, viewport.height, origin, count, cellHeight])

  const jump = (row: number) => {
    const element = scroller.current
    if (!element) return
    const geometry = layout.current
    const line = row + (row >= 0 ? 1 : 0)
    const nextOrigin = Math.max(-geometry.count,
      Math.min(line - Math.floor(geometry.windowRows / 2), geometry.contentEnd - geometry.windowRows))
    anchor.current = { row, offset: 0, left: element.scrollLeft }
    setWindowStart(nextOrigin)
    element.scrollTop = Math.max(0, rowTop(row, nextOrigin, geometry.cellHeight))
    lastTop.current = element.scrollTop
    setViewport((old) => ({ ...old, top: element.scrollTop, left: element.scrollLeft }))
  }

  const find = async (query: string, backwards: boolean): Promise<boolean> => {
    if (!frozen) return false
    const revision = ++searchRevision.current
    const needle = query.toLocaleLowerCase()
    const min = -count
    const max = frozen.rows.length
    const size = max - min
    const prior = searchMatch.current
    const origin = prior?.query === query ? prior.row + (backwards ? -1 : 1) : anchor.current.row
    let index = origin >= max ? min : origin < min ? max - 1 : origin
    let remaining = size
    while (remaining > 0 && mounted.current && revision === searchRevision.current) {
      const batchSize = Math.min(200, remaining, backwards ? index - min + 1 : max - index)
      const start = backwards ? index - batchSize + 1 : index
      const end = start + batchSize
      let archive: Row[]
      try {
        archive = start < 0 ? await cache.readRows(start, Math.min(0, end)) : []
      } catch (error) {
        if (mounted.current && revision === searchRevision.current) setReadError(String(error))
        return false
      }
      const texts = archive.map((row) => ({ index: row.index, text: row.line.text }))
      for (let row = Math.max(0, start); row < end; row++) {
        texts.push({ index: row, text: rowText(frozen.rows[row]) })
      }
      if (backwards) texts.reverse()
      const found = texts.find((row) => row.text.toLocaleLowerCase().includes(needle))
      if (!mounted.current || revision !== searchRevision.current) return false
      if (found) {
        searchMatch.current = { query, row: found.index }
        setMatch(found.index)
        jump(found.index)
        return true
      }
      remaining -= batchSize
      index = backwards ? start - 1 : end
      if (index < min) index = max - 1
      if (index >= max) index = min
      const { promise, resolve } = Promise.withResolvers<void>()
      setTimeout(resolve, 0)
      await promise
    }
    return false
  }

  tools.current = {
    copySelection: () => {
      const selection = window.getSelection()
      if (selection?.anchorNode && scroller.current?.contains(selection.anchorNode)) {
        void copyTerminalText(selection.toString())
      }
    },
    copyScreen: () => {
      const element = scroller.current
      if (!element) return
      const bounds = element.getBoundingClientRect()
      const visible = [...element.querySelectorAll<HTMLElement>('[data-history-row]')].filter((row) => {
        const box = row.getBoundingClientRect()
        return box.bottom > bounds.top && box.top < bounds.bottom
      })
      void copyTerminalText(visible.map((row) => row.textContent ?? '').join('\n'))
    },
    findNext: (query) => find(query, false),
    findPrevious: (query) => find(query, true),
    cancelFind: () => {
      searchRevision.current++
      searchMatch.current = null
      setMatch(null)
    },
    focus: () => scroller.current?.focus({ preventScroll: true }),
  }

  if (restoring) {
    if (restoreError) {
      return (
        <div role="alert" aria-label="Saved terminal view unavailable" className="absolute inset-0 z-10 flex items-center justify-center gap-2 bg-background text-sm text-muted-foreground">
          {restoreError}
          <Button size="sm" variant="ghost" onClick={() => {
            setRestoreError(null)
            setRestoreAttempt((attempt) => attempt + 1)
          }}>Retry</Button>
        </div>
      )
    }
    return <div role="status" aria-label="Restoring saved terminal view" className="absolute inset-0 z-10 flex items-center justify-center bg-background text-sm text-muted-foreground">Restoring saved terminal view</div>
  }
  if (!frozen) return null

  const frozenStart = Math.max(0, startLine - 1)
  const frozenEnd = Math.max(0, endLine - 1)
  const width = Math.max(maxColumns.current * cellWidth + padding * 2,
    anchor.current.left + viewport.width)
  const totalHeight = Math.max(
    lineTop(windowEnd, origin, cellHeight) + padding,
    rowTop(anchor.current.row, origin, cellHeight) + anchor.current.offset + viewport.height,
  )
  return (
    <div
      ref={scroller}
      role="region"
      aria-label="Terminal scrollback"
      data-terminal-history=""
      tabIndex={0}
      className="absolute inset-0 z-10 overflow-auto bg-background text-foreground outline-none"
      style={{ top: padding, bottom: padding, overflowAnchor: 'none', overscrollBehavior: 'contain', touchAction: 'pan-x pan-y' }}
      onScroll={(event) => {
        const element = event.currentTarget
        const down = element.scrollTop > lastTop.current
        if (element.scrollTop !== lastTop.current || element.scrollLeft !== anchor.current.left) searchRevision.current++
        lastTop.current = element.scrollTop
        anchor.current = anchorAt(element.scrollTop, element.scrollLeft, origin, cellHeight)
        setViewport({ top: element.scrollTop, left: element.scrollLeft, height: element.clientHeight, width: element.clientWidth })
        controller.noteViewportInteraction?.()
        if (down && windowEnd === contentEnd && element.scrollTop >= element.scrollHeight - element.clientHeight - 1) {
          returnLive()
        } else if (element.scrollTop < element.clientHeight * 3 ||
          element.scrollTop > element.scrollHeight - element.clientHeight * 4) {
          const line = anchor.current.row + (anchor.current.row >= 0 ? 1 : 0)
          setWindowStart(Math.max(-count, Math.min(line - Math.floor(windowRows / 2), contentEnd - windowRows)))
        }
      }}
      onWheel={(event) => {
        const element = event.currentTarget
        if (event.deltaY > 0 && windowEnd === contentEnd && element.scrollTop >= element.scrollHeight - element.clientHeight - 1) {
          event.preventDefault()
          returnLive()
        }
      }}
      onTouchStart={(event) => { surfaceTouchY.current = event.touches[0]?.clientY ?? null }}
      onTouchMove={(event) => {
        const element = event.currentTarget
        const y = event.touches[0]?.clientY
        if (y !== undefined && surfaceTouchY.current !== null && y < surfaceTouchY.current &&
          windowEnd === contentEnd && element.scrollTop >= element.scrollHeight - element.clientHeight - 1) returnLive()
        surfaceTouchY.current = y ?? null
      }}
      onKeyDown={(event) => {
        const element = event.currentTarget
        const zoom = terminalZoomKey(event.nativeEvent)
        if (zoom) {
          event.preventDefault()
          const state = useStore.getState()
          state.setTerminalFontSize(zoom === 'reset' ? defaultTerminalFontSize : fontSize + (zoom === 'in' ? 1 : -1))
          return
        }
        if (event.ctrlKey && event.shiftKey && event.code === 'KeyF') {
          event.preventDefault()
          controller.setFindOpen(true)
          return
        }
        if (event.ctrlKey && event.shiftKey && !event.altKey && !event.metaKey && event.code === 'KeyC') {
          event.preventDefault()
          tools.current?.copySelection()
          return
        }
        const amount = event.key === 'PageUp' ? -element.clientHeight * 0.9
          : event.key === 'PageDown' ? element.clientHeight * 0.9
          : event.key === 'ArrowUp' ? -cellHeight
          : event.key === 'ArrowDown' ? cellHeight : null
        if (event.key === 'End') {
          event.preventDefault()
          returnLive()
        } else if (event.key === 'Home') {
          event.preventDefault()
          jump(-count)
        } else if (amount !== null) {
          event.preventDefault()
          if (amount > 0 && windowEnd === contentEnd && element.scrollTop + amount >= element.scrollHeight - element.clientHeight) returnLive()
          else element.scrollTop += amount
        }
      }}
    >
      <div style={{ position: 'relative', height: totalHeight, minHeight: '100%', width, minWidth: '100%', fontFamily: frozen.fontFamily, fontSize, letterSpacing: frozen.letterSpacing * scale, lineHeight: `${cellHeight}px` }}>
        {rows.filter((row) => row.index >= archiveStart && row.index < archiveEnd).map(({ index, line }) => (
          <div key={index} data-history-row={index} data-history-cursor={line.cursor}
            className={match === index ? 'bg-accent' : undefined}
            style={{ position: 'absolute', top: rowTop(index, origin, cellHeight), left: padding, height: lineTop(index + 1, origin, cellHeight) - rowTop(index, origin, cellHeight), whiteSpace: 'pre' }}>
            {line.text}
          </div>
        ))}
        {startLine <= 0 && endLine > 0 && (
          <div role="separator" aria-label="Recorded output and live screen boundary" className="absolute border-t border-border text-muted-foreground" style={{ top: lineTop(0, origin, cellHeight), left: padding, right: padding, height: lineTop(1, origin, cellHeight) - lineTop(0, origin, cellHeight), fontSize: Math.min(fontSize, 11), whiteSpace: 'nowrap' }}>
            {frozen.rows.length ? 'Recorded output above · captured terminal screen below' : 'Recorded output above · live terminal below'}
          </div>
        )}
        {frozen.rows.slice(frozenStart, frozenEnd).map((html, offset) => {
          const index = frozenStart + offset
          return <div key={index} data-history-row={index}
            className={match === index ? 'bg-accent' : undefined}
            style={{ position: 'absolute', top: rowTop(index, origin, cellHeight), left: padding, height: rowTop(index + 1, origin, cellHeight) - rowTop(index, origin, cellHeight), whiteSpace: 'pre' }}
            dangerouslySetInnerHTML={{ __html: html }} />
        })}
      </div>
      {(snapshot.loading || snapshot.error || readError) && (
        <div role="status" className="sticky left-0 -mt-6 h-6 w-fit max-w-full bg-background/95 px-2 text-xs text-muted-foreground" style={{ bottom: 0 }}>
          {snapshot.error ?? readError ?? 'Loading older recorded output…'}
          {(snapshot.error || readError) && <Button size="sm" variant="ghost" onClick={() => {
            if (!episodeReady) startArchive()
            else if (readError) setReadAttempt((attempt) => attempt + 1)
            else void cache.loadOlder()
          }}>Retry</Button>}
        </div>
      )}
    </div>
  )
}
