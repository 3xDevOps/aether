import { FitAddon } from '@xterm/addon-fit'
import { SearchAddon } from '@xterm/addon-search'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useCallback, useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import {
  defaultTerminalFontSize,
  terminalFontFamily,
  terminalZoomKey,
  whenTerminalFontReady,
} from '@/lib/term-font'
import { clipboardKeys } from '@/lib/term-clipboard'
import { useStore } from '@/store'

export interface XtermOptions {
  enabled?: boolean
  /**
   * Render at exactly this size instead of fitting the pane, and report no
   * resize. A phone adopts the server's geometry this way and pans the pane
   * over it, so the shared PTY is never reflowed to a phone's width.
   */
  size?: { cols: number; rows: number } | null
  onData?: (data: string) => void
  onResize?: (cols: number, rows: number) => void
  /** Called synchronously before a terminal hyperlink opens. Return true to handle it. */
  onLink?: (uri: string) => boolean
}

export interface XtermController {
  /**
   * Attach to the element the terminal renders into. It is a callback ref,
   * not an object ref, because a caller may mount that element after the
   * terminal is enabled - the environment dock does, while it is still
   * waiting for `terminal.status` - and only a callback ref tells the hook
   * the host has arrived.
   */
  hostRef: React.RefCallback<HTMLDivElement>
  terminal: Terminal | null
  ready: boolean
  /** Backs the find bar `TerminalPane` draws over this terminal. */
  search: SearchAddon | null
  findOpen: boolean
  setFindOpen: (open: boolean) => void
  /** Focuses xterm now, or records the focused action owner until it mounts. */
  focusTerminal: () => void
  /**
   * The key bar's Ctrl modifier. A soft keyboard sends characters rather than
   * key codes, so Ctrl cannot be a held key: armed, it turns the next
   * character - typed or tapped - into its control code, then disarms.
   */
  ctrlArmed: boolean
  armCtrl: (armed: boolean) => void
}

/**
 * The control code a character carries under Ctrl, or null when it has none.
 * `@` through `_` and the letters are the range a terminal maps; anything
 * else, and any multi-character chunk (a paste, an escape sequence from the
 * key bar), goes through untouched.
 */
function controlCode(data: string): string | null {
  if (data.length !== 1) return null
  const code = data.toUpperCase().charCodeAt(0)
  if (code < 0x40 || code > 0x5f) return null
  return String.fromCharCode(code & 0x1f)
}

function rgba(color: string, alpha: number): string | undefined {
  const rgb = color.match(/^rgba?\((.*)\)$/i)
  if (rgb) {
    const channels = rgb[1].replace('/', ' ').trim().split(/[\s,]+/)
    if (channels.length >= 3) {
      const values = channels.slice(0, 3).map((channel) => {
        const value = Number.parseFloat(channel)
        return channel.endsWith('%') ? (value / 100) * 255 : value
      })
      if (values.every((value) => Number.isFinite(value))) {
        return `rgba(${values.map((value) => Math.round(value)).join(', ')}, ${alpha})`
      }
    }
  }

  const hex = color.match(/^#([\da-f]{3,8})$/i)?.[1]
  if (!hex || (hex.length !== 3 && hex.length !== 6)) return undefined
  const expanded = hex.length === 3 ? hex.split('').map((part) => part + part).join('') : hex
  const values = [0, 2, 4].map((at) => Number.parseInt(expanded.slice(at, at + 2), 16))
  return `rgba(${values.join(', ')}, ${alpha})`
}

function oklchRgba(color: string): string | undefined {
  const body = color.match(/^oklch\(([^)]*)\)$/i)?.[1]
  if (!body) return undefined
  const parts = body.replace('/', ' ').trim().split(/\s+/)
  if (parts.length < 3) return undefined

  const lightness = Number.parseFloat(parts[0]) / (parts[0].endsWith('%') ? 100 : 1)
  const chroma = Number.parseFloat(parts[1]) / (parts[1].endsWith('%') ? 100 : 1)
  let hue = Number.parseFloat(parts[2])
  if (parts[2].endsWith('rad')) hue *= 180 / Math.PI
  if (parts[2].endsWith('turn')) hue *= 360
  if (parts[2].endsWith('grad')) hue *= 0.9
  if (![lightness, chroma, hue].every(Number.isFinite)) return undefined

  const radians = (hue * Math.PI) / 180
  const a = chroma * Math.cos(radians)
  const b = chroma * Math.sin(radians)
  const l = lightness + 0.3963377774 * a + 0.2158037573 * b
  const m = lightness - 0.1055613458 * a - 0.0638541728 * b
  const s = lightness - 0.0894841775 * a - 1.291485548 * b
  const linear = [
    4.0767416621 * l ** 3 - 3.3077115913 * m ** 3 + 0.2309699292 * s ** 3,
    -1.2684380046 * l ** 3 + 2.6097574011 * m ** 3 - 0.3413193965 * s ** 3,
    -0.0041960863 * l ** 3 - 0.7034186147 * m ** 3 + 1.707614701 * s ** 3,
  ]
  const channel = (value: number) =>
    Math.round(
      255 *
        Math.max(
          0,
          Math.min(1, value <= 0.0031308 ? 12.92 * value : 1.055 * value ** (1 / 2.4) - 0.055),
        ),
    )
  return `rgba(${linear.map(channel).join(', ')}, 0.25)`
}

function primarySelectionBackground(): string | undefined {
  const root = document.documentElement
  const raw = getComputedStyle(root).getPropertyValue('--primary').trim()
  if (!raw) return undefined

  const probe = document.createElement('span')
  probe.style.color = raw
  probe.style.position = 'absolute'
  const parent = document.body ?? root
  parent.append(probe)
  const resolved = getComputedStyle(probe).color
  probe.remove()
  return rgba(resolved, 0.25) ?? rgba(raw, 0.25) ?? oklchRgba(raw)
}

function paint(host: HTMLDivElement, terminal: Terminal): void {
  const style = getComputedStyle(host)
  if (!style.backgroundColor || !style.color) return
  try {
    const selectionBackground = primarySelectionBackground()
    terminal.options.theme = {
      background: style.backgroundColor,
      foreground: style.color,
      cursor: style.color,
      ...(selectionBackground ? { selectionBackground } : {}),
    }
  } catch {
    // A colour xterm cannot parse is not worth losing the terminal over.
  }
}

export function useXterm({
  enabled = true,
  size = null,
  onData,
  onResize,
  onLink,
}: XtermOptions = {}): XtermController {
  const [host, setHost] = useState<HTMLDivElement | null>(null)
  const onDataRef = useRef(onData)
  const onResizeRef = useRef(onResize)
  const onLinkRef = useRef(onLink)
  const [terminal, setTerminal] = useState<Terminal | null>(null)
  const [search, setSearch] = useState<SearchAddon | null>(null)
  const [findOpen, setFindOpen] = useState(false)
  const focusIntent = useRef<Element | null>(null)
  // Zoom is one preference across every terminal, so it comes from the store
  // rather than from each caller.
  const fontSize = useStore((s) => s.terminalFontSize)
  const fitRef = useRef<FitAddon | null>(null)
  const appliedFontSize = useRef(fontSize)
  const [ctrlArmed, setCtrlArmed] = useState(false)
  const ctrlArmedRef = useRef(false)
  const sizeRef = useRef(size)
  sizeRef.current = size
  onDataRef.current = onData
  onResizeRef.current = onResize
  onLinkRef.current = onLink
  const armCtrl = useCallback((armed: boolean) => {
    ctrlArmedRef.current = armed
    setCtrlArmed(armed)
  }, [])
  const focusTerminal = useCallback(() => {
    const activeElement = document.activeElement
    if (terminal) {
      focusIntent.current = null
      terminal.focus()
      return
    }
    focusIntent.current = activeElement
  }, [terminal])
  useEffect(() => {
    const intent = focusIntent.current
    if (!intent || !terminal) return
    const activeElement = document.activeElement
    // An action can remove or disable its trigger, which sends focus to body.
    // Restore only for that transition; unrelated focus movement cancels it.
    const ownerWasDisabledOrRemoved =
      activeElement === document.body &&
      (!intent.isConnected ||
        (intent instanceof HTMLButtonElement && intent.disabled))
    if (activeElement === intent || ownerWasDisabledOrRemoved) {
      focusIntent.current = null
      terminal.focus()
      return
    }
    focusIntent.current = null
  }, [terminal])
  useEffect(() => {
    const cancelFocusIntent = (event: FocusEvent) => {
      const intent = focusIntent.current
      if (!intent || event.target === intent) return
      const bodyFocusFromOwnerLoss =
        event.target === document.body &&
        (!intent.isConnected ||
          (intent instanceof HTMLButtonElement && intent.disabled))
      if (!bodyFocusFromOwnerLoss) focusIntent.current = null
    }
    document.addEventListener('focusin', cancelFocusIntent)
    return () => document.removeEventListener('focusin', cancelFocusIntent)
  }, [])


  useEffect(() => {
    if (!enabled || !host) return

    // The DOM renderer, deliberately: @xterm/addon-webgl 0.19.0 reuses stale
    // glyph-atlas positions under heavy glyph churn, garbling scrolled rows
    // until a forced refresh (xtermjs/xterm.js#6038; the fix is unreleased).
    // The DOM renderer never desyncs and keeps up with agent TUI streams.
    const openLink = (uri: string) => {
      if (onLinkRef.current?.(uri)) return
      window.open(uri, '_blank', 'noopener,noreferrer')
    }
    const created = new Terminal({
      // Read rather than watched: rebuilding the terminal on a zoom step
      // would throw its scrollback away, so the size is applied below.
      fontSize: (appliedFontSize.current = useStore.getState().terminalFontSize),
      fontFamily: terminalFontFamily,
      scrollback: 50_000,
      cursorBlink: false,
      linkHandler: { activate: (_event, uri) => openLink(uri) },
    })

    let active = true
    let teardown: (() => void) | null = null
    const cancelFontWait = whenTerminalFontReady(() => {
      if (!active) return
      const fit = new FitAddon()
      created.loadAddon(fit)
      created.loadAddon(new WebLinksAddon((_event, uri) => openLink(uri)))
      const searchAddon = new SearchAddon()
      created.loadAddon(searchAddon)
      created.open(host)
      fitRef.current = fit

      // xterm keeps a single custom key handler, so zoom, find and the
      // clipboard shortcuts are one chain: the first to claim the event stops
      // it reaching the shell.
      const clipboard = clipboardKeys(created)
      created.attachCustomKeyEventHandler((ev) => {
        if (ev.type !== 'keydown') return clipboard(ev)
        const zoom = terminalZoomKey(ev)
        if (zoom) {
          ev.preventDefault()
          const { terminalFontSize, setTerminalFontSize } = useStore.getState()
          setTerminalFontSize(
            zoom === 'reset'
              ? defaultTerminalFontSize
              : terminalFontSize + (zoom === 'in' ? 1 : -1),
          )
          return false
        }
        if (ev.ctrlKey && ev.shiftKey && ev.code === 'KeyF') {
          ev.preventDefault()
          setFindOpen(true)
          return false
        }
        return clipboard(ev)
      })

      const repaint = () => paint(host, created)
      repaint()
      const themeWatch = new MutationObserver(repaint)
      themeWatch.observe(document.documentElement, { attributeFilter: ['class'] })

      // A fixed size is the caller's, not this pane's, so the pane's own
      // measurements are ignored and nothing is reported back: reporting is
      // what sends a resize to the shared PTY.
      const resize = () => {
        const fixed = sizeRef.current
        if (fixed) {
          created.resize(fixed.cols, fixed.rows)
          return
        }
        fit.fit()
        onResizeRef.current?.(created.cols, created.rows)
      }
      resize()

      // At a fixed size the grid is larger than the pane, so the row being
      // typed on can sit outside it. A tap is the moment that matters: it is
      // what raises the keyboard. `scrollIntoView` is absent in jsdom, and
      // the cursor cell only exists once a renderer has drawn one.
      const showCursor = () => {
        requestAnimationFrame(() => {
          host
            .querySelector('.xterm-cursor')
            ?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
        })
      }
      host.addEventListener('focusin', showCursor)

      const input = created.onData((data) => {
        if (!ctrlArmedRef.current) {
          onDataRef.current?.(data)
          return
        }
        armCtrl(false)
        onDataRef.current?.(controlCode(data) ?? data)
      })
      const observer = new ResizeObserver(resize)
      observer.observe(host)
      teardown = () => {
        host.removeEventListener('focusin', showCursor)
        observer.disconnect()
        themeWatch.disconnect()
        input.dispose()
        fitRef.current = null
      }
      setTerminal(created)
      setSearch(searchAddon)
    })

    return () => {
      active = false
      armCtrl(false)
      cancelFontWait()
      teardown?.()
      created.dispose()
      setTerminal(null)
      setSearch(null)
      setFindOpen(false)
    }
  }, [armCtrl, enabled, host])

  // The server's geometry arrives with the attach ack, after the terminal was
  // built, and changes again on a reattach. Leaving a fixed size behind hands
  // the pane back to the fit addon, which the next observed resize runs.
  useEffect(() => {
    if (!terminal || !size) return
    terminal.resize(size.cols, size.rows)
  }, [size?.cols, size?.rows, terminal])

  // A zoom step changes the cell size, so the pane has to be re-fitted and
  // the new geometry sent to the shell; nothing else observes the resize. A
  // terminal that was just built at this size is already fitted. At a fixed
  // size zoom only changes how much of the same grid fits on screen.
  useEffect(() => {
    if (!terminal || appliedFontSize.current === fontSize) return
    appliedFontSize.current = fontSize
    terminal.options.fontSize = fontSize
    if (sizeRef.current) return
    fitRef.current?.fit()
    onResizeRef.current?.(terminal.cols, terminal.rows)
  }, [fontSize, terminal])

  return {
    hostRef: setHost,
    terminal,
    ready: terminal !== null,
    search,
    findOpen,
    setFindOpen,
    focusTerminal,
    ctrlArmed,
    armCtrl,
  }
}
