import type { IBufferCell, IBufferLine, Terminal } from '@xterm/xterm'

export interface FrozenTerminal {
  /** Standalone safe HTML row fragments, never unescaped PTY output. */
  rows: string[]
  cols: number
  viewportY: number
  baseY: number
  cellWidth: number
  cellHeight: number
  fontFamily: string
  fontSize: number
  letterSpacing: number
}

function renderedColors(screen: HTMLElement): {
  palette: Map<number, string>
  dimPalette: Map<number, string>
  foreground: string
  dimForeground: string
} {
  const palette = new Map<number, string>()
  const dimPalette = new Map<number, string>()
  let foreground = ''
  let dimForeground = ''
  for (const style of screen.querySelectorAll('style')) {
    // React removes the host before passive-effect cleanup. A detached style
    // has no sheet; parse its retained CSS with the browser's CSSOM instead.
    const detached = style.sheet ? null : style.cloneNode(true) as HTMLStyleElement
    if (detached) screen.ownerDocument.head.append(detached)
    try {
      for (const rule of Array.from((detached ?? style).sheet?.cssRules ?? [])) {
        if (!('selectorText' in rule)) continue
        const css = rule as CSSStyleRule
        const match = / \.xterm-fg-(\d+)(\.xterm-dim)?$/.exec(css.selectorText)
        if (match) (match[2] ? dimPalette : palette).set(Number(match[1]), css.style.color)
        if (css.selectorText.endsWith(' .xterm-rows') && css.style.color) foreground = css.style.color
        if (css.selectorText.endsWith(' .xterm-rows .xterm-dim')) dimForeground = css.style.color
      }
    } finally {
      detached?.remove()
    }
  }
  return { palette, dimPalette, foreground, dimForeground }
}

function cellColor(
  cell: IBufferCell,
  foreground: boolean,
  fallback: string,
  palette: Map<number, string>,
  bright: boolean,
): string {
  const color = foreground ? cell.getFgColor() : cell.getBgColor()
  if (foreground ? cell.isFgRGB() : cell.isBgRGB()) {
    return `#${color.toString(16).padStart(6, '0')}`
  }
  if (foreground ? cell.isFgPalette() : cell.isBgPalette()) {
    return palette.get(color < 8 && bright ? color + 8 : color) ?? fallback
  }
  return fallback
}

function escapeHTML(text: string): string {
  return text.replace(/[&<>"']/g, (character) => {
    switch (character) {
      case '&': return '&amp;'
      case '<': return '&lt;'
      case '>': return '&gt;'
      case '"': return '&quot;'
      default: return '&#39;'
    }
  })
}

interface RenderedDecoration {
  start: number
  end: number
  css: string
}

function renderedDecorations(
  row: Element | undefined,
  line: IBufferLine,
  cell: IBufferCell,
  measure: HTMLElement,
): RenderedDecoration[] {
  if (!row) return []
  const spans: { start: number; end: number; element: HTMLElement }[] = []
  let column = 0
  for (const child of row.children) {
    const element = child as HTMLElement
    const text = element.textContent ?? ''
    const start = column
    for (let offset = 0; offset < text.length;) {
      if (column >= line.length) return []
      line.getCell(column, cell)
      const width = cell.getWidth()
      if (!width) {
        column++
        continue
      }
      const chars = cell.isInvisible() ? ' ' : cell.getChars() || ' '
      // Walk cells, not codepoints or text searches: combined/wide glyphs and
      // adjacent equal text with different decorations must keep their columns.
      if (!text.startsWith(chars, offset) && !(chars === ' ' && text[offset] === '\u00a0')) return []
      offset += chars.length
      column += width
    }
    if (element.className.includes('xterm-underline-')) spans.push({ start, end: column, element })
  }
  return spans.map(({ start, end, element }) => {
    // A detached terminal no longer has computed styles. Its retained classes
    // and inline decoration color can still be resolved by the loaded xterm CSS.
    const clone = element.isConnected ? null : element.cloneNode(false) as HTMLElement
    if (clone) measure.append(clone)
    try {
      const computed = getComputedStyle(clone ?? element)
      const css = (computed.textDecorationLine ? `text-decoration-line:${computed.textDecorationLine};` : '') +
        (computed.textDecorationStyle ? `text-decoration-style:${computed.textDecorationStyle};` : '') +
        (element.style.textDecorationColor ? `text-decoration-color:${element.style.textDecorationColor};` : '')
      return { start, end, css }
    } finally {
      clone?.remove()
    }
  })
}

/** Empty rows mean no capturable normal screen; do not overwrite a saved view. */
export function captureTerminalPresentation(terminal: Terminal): FrozenTerminal {
  const options = terminal.options
  // The public options type is partial even though xterm's OptionsService
  // supplies these defaults before opening a terminal.
  const frozen: FrozenTerminal = {
    rows: [],
    cols: terminal.cols,
    viewportY: 0,
    baseY: 0,
    cellWidth: 0,
    cellHeight: 0,
    fontFamily: options.fontFamily ?? 'monospace',
    fontSize: options.fontSize ?? 15,
    letterSpacing: options.letterSpacing ?? 0,
  }
  const element = terminal.element
  const screen = element?.querySelector<HTMLElement>('.xterm-screen')
  const renderedRows = element?.querySelector<HTMLElement>('.xterm-rows')
  // Disposal removes the terminal element; React may already have detached its
  // host during cleanup, so isConnected would incorrectly reject that capture.
  if (!element?.parentElement || !screen || !renderedRows) return frozen
  const buffer = terminal.buffer.normal
  if (terminal.buffer.active !== buffer) return frozen

  const bounds = screen.getBoundingClientRect()
  const renderedRow = renderedRows.firstElementChild as HTMLElement | null
  frozen.cellWidth = (Number.parseFloat(screen.style.width) || bounds.width) / terminal.cols
  frozen.cellHeight = renderedRow?.getBoundingClientRect().height ||
    Number.parseFloat(renderedRow?.style.height ?? '') ||
    (Number.parseFloat(screen.style.height) || bounds.height) / terminal.rows
  if (!(frozen.cellWidth > 0 && frozen.cellHeight > 0)) return frozen
  const renderedStyle = getComputedStyle(renderedRows)
  // The measured correction, unlike the option, survives detached cleanup.
  frozen.letterSpacing = Number.parseFloat(renderedRows.style.letterSpacing || renderedStyle.letterSpacing) || 0
  frozen.viewportY = buffer.viewportY
  frozen.baseY = buffer.baseY
  const colors = renderedColors(screen)
  const foreground = colors.foreground || renderedStyle.color || options.theme?.foreground || '#ffffff'
  const background = screen.parentElement?.style.backgroundColor || options.theme?.background || '#000000'
  const invertedBackground = colors.palette.get(257) || background
  const weight = options.fontWeight ?? 'normal'
  const boldWeight = options.fontWeightBold ?? 'bold'
  const fontVariant = renderedRows.style.fontVariant || renderedStyle.fontVariant || 'normal'
  const spacingTrim = renderedRows.style.getPropertyValue('text-spacing-trim') ||
    renderedStyle.getPropertyValue('text-spacing-trim') || 'space-all'
  const lineHeight = Number.parseFloat(renderedRow?.style.lineHeight ?? '') || frozen.cellHeight
  const rowStyle = `color:${foreground};background-color:${background};white-space:pre;font-kerning:none;font-variant:${fontVariant};text-spacing-trim:${spacingTrim};height:100%;line-height:${lineHeight / frozen.fontSize}em;`
  const boxStyle = 'display:inline-block;height:100%;vertical-align:top;'

  // Match the DOM renderer's WidthCache measurement (32 repeats, offsetWidth).
  // Measure only distinct glyph/font variants, not cells; equal-advance glyphs
  // then share one span rather than expanding a dense buffer into a million boxes.
  const measure = element.ownerDocument.createElement('span')
  measure.style.cssText = `position:absolute;visibility:hidden;white-space:pre;line-height:normal;letter-spacing:0;font-kerning:none;font-variant:${fontVariant};text-spacing-trim:${spacingTrim};`
  measure.style.fontFamily = frozen.fontFamily
  measure.style.fontSize = `${frozen.fontSize}px`
  element.ownerDocument.body.append(measure)
  const advances = Array.from({ length: 4 }, () => new Map<string, number>())
  const cell = buffer.getNullCell()
  let previousAttributes = ''
  let previousDecoration = ''
  let style = ''
  let font = 0
  try {
    let backingStart = ''
    let backingEnd = ''
    if (options.allowTransparency) {
      // xterm retains a legacy viewport behind its new scrollable surface.
      // It is a painted sibling, not an ancestor of the rendered rows.
      const viewport = element.querySelector<HTMLElement>('.xterm-viewport')
      if (viewport) {
        const detached = viewport.isConnected ? null : element.cloneNode(false) as HTMLElement
        const target = detached ? viewport.cloneNode(false) as HTMLElement : viewport
        if (detached) {
          detached.append(target)
          measure.append(detached)
        }
        const backing = getComputedStyle(target).backgroundColor
        detached?.remove()
        if (backing && backing !== 'rgba(0, 0, 0, 0)' && backing !== 'transparent') {
          backingStart = `<div style="height:100%;background-color:${escapeHTML(backing)}">`
          backingEnd = '</div>'
        }
      }
    }
    // Detached DOM has no layout. Re-measure its retained row height in the
    // document so attached/detached captures use the same CSS pixel rounding.
    if (renderedRow?.style.height && !renderedRow.isConnected) {
      const rowMeasure = element.ownerDocument.createElement('div')
      rowMeasure.style.height = renderedRow.style.height
      measure.append(rowMeasure)
      frozen.cellHeight = rowMeasure.getBoundingClientRect().height || frozen.cellHeight
      rowMeasure.remove()
    }
    for (let index = 0; index < buffer.length; index++) {
      const line = buffer.getLine(index)!
      // Only rendered rows expose extended underline attributes through a
      // supported interface. Offscreen rows retain their public buffer styles.
      // Callers must capture after onRender; refresh() only schedules a frame.
      const decorations = renderedDecorations(
        renderedRows.children[index - frozen.viewportY], line, cell, measure,
      )
      let decorationIndex = 0
      let html = `${backingStart}<div style="${escapeHTML(rowStyle)}">`
      let run = ''
      let runWidth = 0
      let runSpacing = 0
      let runStyle = ''
      const flush = () => {
        if (!run) return
        html += `<span style="${boxStyle}width:${runWidth * frozen.cellWidth / frozen.fontSize}em;letter-spacing:${runSpacing / frozen.fontSize}em;${runStyle}">${escapeHTML(run)}</span>`
        run = ''
        runWidth = 0
      }
      for (let column = 0; column < line.length; column++) {
        line.getCell(column, cell)
        const width = cell.getWidth()
        if (!width) continue
        const chars = cell.getChars() || ' '
        while (decorationIndex < decorations.length && decorations[decorationIndex].end <= column) decorationIndex++
        const renderedDecoration = decorations[decorationIndex]
        const extendedDecoration = renderedDecoration && renderedDecoration.start <= column ? renderedDecoration.css : ''
        // Ordinary output avoids allocating attribute keys and resolving CSS for
        // every cell. Styled runs likewise resolve their CSS only on a transition.
        const attributes = cell.isAttributeDefault() ? '' :
          `${cell.getFgColorMode()}:${cell.getFgColor()}:${cell.getBgColorMode()}:${cell.getBgColor()}:${cell.isBold()}:${cell.isItalic()}:${cell.isDim()}:${cell.isInverse()}:${cell.isInvisible()}:${cell.isUnderline()}:${cell.isOverline()}:${cell.isStrikethrough()}`
        if (attributes !== previousAttributes || extendedDecoration !== previousDecoration || !style) {
          previousAttributes = attributes
          previousDecoration = extendedDecoration
          const inverse = !!cell.isInverse()
          const bold = !!cell.isBold()
          const italic = !!cell.isItalic()
          font = Number(bold) | (Number(italic) << 1)
          // Native inline RGB wins over the DIM class; palette/default colors
          // use the renderer's exact alpha, including its 8-bit rounding.
          const dim = !!cell.isDim() && !(inverse ? cell.isBgRGB() : cell.isFgRGB())
          const palette = dim ? colors.dimPalette : colors.palette
          const defaultColor = inverse ? (dim ? palette.get(257) : invertedBackground) :
            (dim ? colors.dimForeground : foreground)
          let color = cellColor(cell, !inverse, defaultColor || (inverse ? invertedBackground : foreground),
            palette, bold && (options.drawBoldTextInBrightColors ?? true))
          if (cell.isInvisible()) color = 'transparent'
          const bg = !inverse && cell.isBgDefault() ? 'transparent' :
            cellColor(cell, inverse, inverse ? foreground : background, colors.palette, false)
          const decoration = [cell.isUnderline() ? 'underline' : '', cell.isOverline() ? 'overline' : '',
            cell.isStrikethrough() ? 'line-through' : ''].filter(Boolean).join(' ') || 'none'
          style = escapeHTML(`color:${color};background-color:${bg};font-weight:${bold ? boldWeight : weight};font-style:${italic ? 'italic' : 'normal'};text-decoration:${decoration};${extendedDecoration}visibility:visible;`)
        }
        // Combined glyphs get one explicit cell box: letter-spacing is per
        // character, not per buffer cell. Single-codepoint runs can be grouped.
        const combined = chars.length > (chars.codePointAt(0)! > 0xffff ? 2 : 1)
        let spacing = 0
        if (!combined) {
          const cache = advances[font]
          let advance = cache.get(chars)
          if (advance === undefined) {
            measure.style.fontWeight = String(font & 1 ? boldWeight : weight)
            measure.style.fontStyle = font & 2 ? 'italic' : 'normal'
            measure.textContent = chars.repeat(32)
            advance = measure.offsetWidth / 32
            // Layout-less documents cannot prove equal advances for Unicode.
            // Keep explicit boxes there; real rendered terminals measure them.
            if (!advance) advance = chars.charCodeAt(0) < 0x7f ? frozen.cellWidth - frozen.letterSpacing : 0
            cache.set(chars, advance)
          }
          spacing = advance ? width * frozen.cellWidth - advance : 0
          if (!advance) {
            flush()
            run = chars
            runWidth = width
            runSpacing = 0
            runStyle = style
            flush()
            continue
          }
        }
        if (combined || runStyle !== style || runSpacing !== spacing) flush()
        run += chars
        runWidth += width
        runSpacing = spacing
        runStyle = style
        if (combined) flush()
      }
      flush()
      frozen.rows.push(`${html}</div>${backingEnd}`)
    }
  } finally {
    measure.remove()
  }
  return frozen
}
