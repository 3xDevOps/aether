import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { terminalFontFamily, whenTerminalFontReady } from '@/lib/term-font'
import { attachClipboardKeys } from '@/lib/term-clipboard'

export interface XtermOptions {
  enabled?: boolean
  onData?: (data: string) => void
  onResize?: (cols: number, rows: number) => void
}

export interface XtermController {
  hostRef: React.RefObject<HTMLDivElement | null>
  terminal: Terminal | null
  ready: boolean
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
  onData,
  onResize,
}: XtermOptions = {}): XtermController {
  const hostRef = useRef<HTMLDivElement>(null)
  const onDataRef = useRef(onData)
  const onResizeRef = useRef(onResize)
  const [terminal, setTerminal] = useState<Terminal | null>(null)
  onDataRef.current = onData
  onResizeRef.current = onResize

  useEffect(() => {
    if (!enabled) return
    const host = hostRef.current
    if (!host) return

    // The DOM renderer, deliberately: @xterm/addon-webgl 0.19.0 reuses stale
    // glyph-atlas positions under heavy glyph churn, garbling scrolled rows
    // until a forced refresh (xtermjs/xterm.js#6038; the fix is unreleased).
    // The DOM renderer never desyncs and keeps up with agent TUI streams.
    const created = new Terminal({
      fontSize: 12,
      fontFamily: terminalFontFamily,
      scrollback: 50_000,
      cursorBlink: false,
    })

    let active = true
    let teardown: (() => void) | null = null
    const cancelFontWait = whenTerminalFontReady(() => {
      if (!active) return
      const fit = new FitAddon()
      created.loadAddon(fit)
      created.open(host)
      attachClipboardKeys(created)

      const repaint = () => paint(host, created)
      repaint()
      const themeWatch = new MutationObserver(repaint)
      themeWatch.observe(document.documentElement, { attributeFilter: ['class'] })

      const resize = () => {
        fit.fit()
        onResizeRef.current?.(created.cols, created.rows)
      }
      resize()

      const input = created.onData((data) => onDataRef.current?.(data))
      const observer = new ResizeObserver(resize)
      observer.observe(host)
      teardown = () => {
        observer.disconnect()
        themeWatch.disconnect()
        input.dispose()
      }
      setTerminal(created)
    })

    return () => {
      active = false
      cancelFontWait()
      teardown?.()
      created.dispose()
      setTerminal(null)
    }
  }, [enabled])

  return { hostRef, terminal, ready: terminal !== null }
}
