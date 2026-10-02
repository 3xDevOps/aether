import { Terminal } from '@xterm/xterm'
import { captureTerminalPresentation, type FrozenTerminal } from './terminal-presentation'

const terminals: Terminal[] = []
const hosts: HTMLElement[] = []

function openTerminal(cols = 8, rows = 4): Terminal {
  const host = document.createElement('div')
  document.body.append(host)
  hosts.push(host)
  const terminal = new Terminal({
    cols,
    rows,
    scrollback: 5,
    fontSize: 12,
    fontFamily: 'monospace',
    theme: { foreground: '#eeeeee', background: '#111111' },
  })
  terminal.open(host)
  terminals.push(terminal)
  supplyRenderedMeasurements(terminal)
  return terminal
}

function supplyRenderedMeasurements(terminal: Terminal): void {
  // jsdom has no font layout. Supply the DOM measurements capture reads;
  // browser regressions cover real glyph and viewport pixel positions.
  const screen = terminal.element!.querySelector<HTMLElement>('.xterm-screen')!
  screen.style.width = `${terminal.cols * 8.25}px`
  screen.style.height = `${terminal.rows * 17.5}px`
  terminal.element!.querySelector<HTMLElement>('.xterm-rows')!.style.letterSpacing = '0.125px'
}

function renderFrozen(frozen: FrozenTerminal): HTMLElement {
  const host = document.createElement('div')
  host.style.fontFamily = frozen.fontFamily
  host.style.fontSize = `${frozen.fontSize}px`
  host.style.letterSpacing = `${frozen.letterSpacing}px`
  for (const html of frozen.rows) {
    const row = document.createElement('div')
    row.style.width = `${frozen.cellWidth * frozen.cols}px`
    row.style.height = `${frozen.cellHeight}px`
    row.innerHTML = html
    host.append(row)
  }
  document.body.append(host)
  hosts.push(host)
  return host
}

async function writeTerminal(terminal: Terminal, data: string): Promise<void> {
  await new Promise<void>((resolve) => terminal.write(data, resolve))
}

afterEach(() => {
  for (const terminal of terminals.splice(0)) terminal.dispose()
  for (const host of hosts.splice(0)) host.remove()
})

describe('frozen terminal presentation', () => {
  it('retains blank and wrapped rows with wide and combined glyph columns after disposal', async () => {
    const terminal = openTerminal()
    await writeTerminal(terminal, 'a界e\u0301Z\r\n\r\n123456789')
    const frozen = captureTerminalPresentation(terminal)
    terminal.reset()
    terminal.dispose()
    const surface = renderFrozen(frozen)

    expect(Array.from(surface.children, (row) => row.textContent)).toEqual([
      'a界e\u0301Z   ',
      '        ',
      '12345678',
      '9       ',
    ])
    expect(frozen.cellWidth).toBe(8.25)
    expect(frozen.cellHeight).toBe(17.5)
    expect(frozen.letterSpacing).toBe(0.125)
    const glyphs = Array.from(surface.children[0].querySelectorAll('span'))
    const wide = glyphs.find((span) => span.textContent === '界')!
    const combining = glyphs.find((span) => span.textContent === 'e\u0301')!
    expect(Number.parseFloat(wide.style.width) * frozen.fontSize).toBe(2 * frozen.cellWidth)
    expect(Number.parseFloat(combining.style.width) * frozen.fontSize).toBe(frozen.cellWidth)
    let column = 0
    const columns = glyphs.filter((span) => !span.children.length).map((span) => {
      const start = column
      column += Number.parseFloat(span.style.width) * frozen.fontSize / frozen.cellWidth
      return { text: span.textContent, start }
    })
    expect(columns).toEqual([
      { text: 'a', start: 0 },
      { text: '界', start: 1 },
      { text: 'e\u0301', start: 3 },
      { text: 'Z   ', start: 4 },
    ])
    expect(column).toBe(frozen.cols)
  })

  it('escapes terminal text instead of restoring markup or interactive links', async () => {
    const terminal = openTerminal(80, 2)
    const text = '界<img src=x onerror="alert(1)"> &\u0301 <script>bad()</script>\ue0b0 literal\u00a0space'
    await writeTerminal(terminal, `\x1b]8;;https://example.test\x07${text}\x1b]8;;\x07`)
    const surface = renderFrozen(captureTerminalPresentation(terminal))

    expect(surface.children[0].textContent?.trimEnd()).toBe(text)
    expect(surface.querySelector('img, script, a, [onerror]')).toBeNull()
  })

  it('carries styles across wrapped rows and retains real inverse and dim colors', async () => {
    const terminal = openTerminal(6, 4)
    await writeTerminal(terminal,
      '\x1b[38;2;10;20;30;1;3;4mabcdefghi\x1b[0m\r\n' +
      '\x1b[38;2;17;34;51;48;2;68;85;102;7mI\x1b[0m' +
      '\x1b[38;2;100;120;140;48;2;20;30;40;2mD\x1b[8mH',
    )
    const surface = renderFrozen(captureTerminalPresentation(terminal))
    const wrapped = Array.from(surface.children[1].querySelectorAll('span'))
      .find((span) => span.textContent === 'ghi')!
    const inverse = Array.from(surface.children[2].querySelectorAll('span'))
      .find((span) => span.textContent === 'I')!
    const dim = Array.from(surface.children[2].querySelectorAll('span'))
      .find((span) => span.textContent?.startsWith('D'))!
    const hidden = Array.from(surface.children[2].querySelectorAll('span'))
      .find((span) => span.textContent?.startsWith('H'))!

    expect(getComputedStyle(wrapped).color).toBe('rgb(10, 20, 30)')
    expect(getComputedStyle(wrapped).fontWeight).toBe('bold')
    expect(getComputedStyle(wrapped).fontStyle).toBe('italic')
    expect(getComputedStyle(wrapped).textDecoration).toContain('underline')
    expect(getComputedStyle(inverse).color).toBe('rgb(68, 85, 102)')
    expect(getComputedStyle(inverse).backgroundColor).toBe('rgb(17, 34, 51)')
    expect(getComputedStyle(dim).color).toBe('rgb(100, 120, 140)')
    expect(getComputedStyle(dim).backgroundColor).toBe('rgb(20, 30, 40)')
    expect(getComputedStyle(hidden).color).toBe('rgba(0, 0, 0, 0)')
    expect(getComputedStyle(hidden).backgroundColor).toBe('rgb(20, 30, 40)')
    expect(getComputedStyle(hidden).visibility).toBe('visible')
  })

  it('dims palette and default colors but keeps an inverse RGB foreground opaque', async () => {
    const terminal = openTerminal(8, 2)
    terminal.options.theme = { foreground: '#eeeeee', background: '#111111', red: '#123456' }
    supplyRenderedMeasurements(terminal)
    await writeTerminal(terminal,
      '\x1b[2mD\x1b[31mP\x1b[38;2;100;120;140;48;2;20;30;40;7mR' +
      '\x1b[41mI\x1b[49mB',
    )
    const surface = renderFrozen(captureTerminalPresentation(terminal))
    const color = (text: string) => getComputedStyle(
      Array.from(surface.children[0].querySelectorAll('span')).find((span) => span.textContent === text)!,
    ).color

    const reference = document.createElement('span').style
    for (const [text, css] of Object.entries({
      D: '#eeeeee80', P: '#12345680', R: '#141e28', I: '#12345680', B: '#11111180',
    })) {
      reference.color = css
      expect(color(text)).toBe(reference.color)
    }
  })

  it('paints a translucent default background once while retaining explicit cell backgrounds', async () => {
    const terminal = openTerminal(8, 2)
    terminal.options.allowTransparency = true
    terminal.options.theme = { foreground: '#eeeeee', background: '#11223380' }
    supplyRenderedMeasurements(terminal)
    await writeTerminal(terminal, 'D\x1b[48;2;10;20;30mE\x1b[0;7mI\x1b[0m')
    const surface = renderFrozen(captureTerminalPresentation(terminal))
    const row = surface.children[0].firstElementChild!
    const background = (text: string) => getComputedStyle(
      Array.from(row.querySelectorAll('span')).find((span) => span.textContent === text)!,
    ).backgroundColor

    const reference = document.createElement('span').style
    reference.backgroundColor = '#11223380'
    expect(getComputedStyle(row).backgroundColor).toBe(reference.backgroundColor)
    expect(background('D')).toBe('rgba(0, 0, 0, 0)')
    expect(background('E')).toBe('rgb(10, 20, 30)')
    expect(background('I')).toBe('rgb(238, 238, 238)')
  })

  it('keeps the rendered palette and glyph spacing after React detaches the host', async () => {
    const terminal = openTerminal(8, 2)
    terminal.options.theme = {
      foreground: '#eef0f2',
      background: '#101214',
      red: '#113355',
      brightRed: '#7799bb',
    }
    // Updating any option refreshes the renderer's dimensions and measured
    // spacing. Supply the layout fixture after that refresh, not before it.
    supplyRenderedMeasurements(terminal)
    await writeTerminal(terminal, '\x1b[31;1mB\x1b[22;7mI\x1b[0mD')
    terminal.element!.parentElement!.remove()
    // Chromium no longer computes styles for a detached subtree. xterm's
    // measured inline spacing must survive even though its option is zero.
    const computedStyle = vi.spyOn(window, 'getComputedStyle')
      .mockReturnValue(document.createElement('div').style)
    let frozen: FrozenTerminal
    try {
      frozen = captureTerminalPresentation(terminal)
    } finally {
      computedStyle.mockRestore()
    }
    expect(frozen.letterSpacing).toBe(0.125)
    const surface = renderFrozen(frozen)
    const spans = Array.from(surface.children[0].querySelectorAll('span'))
    const bold = spans.find((span) => span.textContent === 'B')!
    const inverse = spans.find((span) => span.textContent === 'I')!
    const normal = spans.find((span) => span.textContent?.startsWith('D'))!

    expect(getComputedStyle(bold).color).toBe('rgb(119, 153, 187)')
    expect(getComputedStyle(inverse).color).toBe('rgb(16, 18, 20)')
    expect(getComputedStyle(inverse).backgroundColor).toBe('rgb(17, 51, 85)')
    expect(getComputedStyle(normal).color).toBe('rgb(238, 240, 242)')
  })

  it('keeps styled Unicode cells and adjacent ASCII on their terminal columns', async () => {
    const terminal = openTerminal(8, 2)
    await writeTerminal(terminal, '\x1b[38;2;10;20;30;48;2;40;50;60;1;3;4m界e\u0301\ue0b0"\x1b[0mZ')
    const frozen = captureTerminalPresentation(terminal)
    const surface = renderFrozen(frozen)
    const leaves = Array.from(surface.children[0].querySelectorAll('span'))
      .filter((span) => !span.children.length)
    const textColumns = new Map<string, number>()
    let column = 0
    for (const span of leaves) {
      textColumns.set(span.textContent!, column)
      column += Number.parseFloat(span.style.width) * frozen.fontSize / frozen.cellWidth
    }
    expect(textColumns.get('界')).toBe(0)
    expect(textColumns.get('e\u0301')).toBe(2)
    expect(textColumns.get('\ue0b0')).toBe(3)
    expect(textColumns.get('"')).toBe(4)
    expect(textColumns.get('Z  ')).toBe(5)
    expect(column).toBe(frozen.cols)
    const styled = leaves.find((span) => span.textContent === '界')!
    expect(getComputedStyle(styled).color).toBe('rgb(10, 20, 30)')
    expect(getComputedStyle(styled).backgroundColor).toBe('rgb(40, 50, 60)')
    expect(getComputedStyle(styled).fontWeight).toBe('bold')
    expect(getComputedStyle(styled).fontStyle).toBe('italic')
    expect(getComputedStyle(styled).textDecoration).toContain('underline')
  })

  it('retains every bounded buffer row at its original index after live output moves on', async () => {
    const terminal = openTerminal(8, 3)
    await writeTerminal(terminal, Array.from({ length: 12 }, (_, index) => `row-${index}\r\n`).join(''))
    const buffer = terminal.buffer.normal
    const expected = Array.from({ length: buffer.length }, (_, index) =>
      buffer.getLine(index)!.translateToString(),
    )
    const frozen = captureTerminalPresentation(terminal)
    const baseY = buffer.baseY
    const viewportY = buffer.viewportY
    await writeTerminal(terminal, 'later\r\n'.repeat(12))
    terminal.reset()
    const surface = renderFrozen(frozen)

    expect(Array.from(surface.children, (row) => row.textContent)).toEqual(expected)
    expect(frozen.rows).toHaveLength(8)
    expect(frozen.baseY).toBe(baseY)
    expect(frozen.viewportY).toBe(viewportY)
    expect(frozen.cols).toBe(8)
  })

  it('leaves a saved screen intact when a replacement is unopened, alternate, or disposed', async () => {
    const terminal = openTerminal()
    await writeTerminal(terminal, 'saved')
    const saved = captureTerminalPresentation(terminal)
    const unopened = new Terminal()
    terminals.push(unopened)
    const candidates = [captureTerminalPresentation(unopened)]
    await writeTerminal(terminal, '\x1b[?1049halt')
    candidates.push(captureTerminalPresentation(terminal))
    terminal.dispose()
    candidates.push(captureTerminalPresentation(terminal))
    let retained = saved
    for (const candidate of candidates) {
      if (candidate.rows.length) retained = candidate
    }

    expect(renderFrozen(retained).children[0].textContent?.trimEnd()).toBe('saved')
    expect(candidates.map((candidate) => candidate.rows)).toEqual([[], [], []])
  })
})
