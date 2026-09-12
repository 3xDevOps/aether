import { act, fireEvent, render, screen } from '@testing-library/react'
import { useXterm } from '@/components/xterm-host'
import { TerminalPane } from '@/components/terminal-pane'

vi.mock('@xterm/xterm', () => {
  class MockTerminal {
    cols = 80
    rows = 24
    options: { fontSize: number; theme?: unknown }
    private input: HTMLTextAreaElement | null = null

    constructor(options: { fontSize?: number } = {}) {
      this.options = { fontSize: options.fontSize ?? 12 }
    }

    loadAddon() {}

    open(host: HTMLElement) {
      this.input = document.createElement('textarea')
      this.input.setAttribute('aria-label', 'Terminal input')
      host.append(this.input)
    }

    focus() {
      this.input?.focus()
    }

    attachCustomKeyEventHandler() {}

    onSelectionChange() {
      return { dispose() {} }
    }

    onData() {
      return { dispose() {} }
    }

    onCursorMove() {
      return { dispose() {} }
    }

    dispose() {
      this.input?.remove()
      this.input = null
    }
  }

  return { Terminal: MockTerminal }
})

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))

vi.mock('@xterm/addon-search', () => ({
  SearchAddon: class {
    findNext() {
      return false
    }

    findPrevious() {
      return false
    }
  },
}))

vi.mock('@xterm/addon-web-links', () => ({
  WebLinksAddon: class {},
}))

class NoResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function PaneProbe() {
  return <TerminalPane controller={useXterm()} />
}

function PaneWithExternalControl() {
  return (
    <>
      <PaneProbe />
      <button type="button">External control</button>
    </>
  )
}

const originalFonts = Object.getOwnPropertyDescriptor(document, 'fonts')

function mountWithPendingFonts(ui = <PaneProbe />) {
  const { promise, resolve } = Promise.withResolvers<void>()
  Object.defineProperty(document, 'fonts', {
    configurable: true,
    value: {
      check: () => false,
      load: () => promise,
    },
  })
  return { resolveFonts: resolve, fontsReady: promise, view: render(ui) }
}

async function releaseFonts(resolveFonts: () => void, fontsReady: Promise<void>) {
  await act(async () => {
    resolveFonts()
    await fontsReady
    await Promise.resolve()
    await Promise.resolve()
  })
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', NoResizeObserver)
})

afterEach(() => {
  vi.unstubAllGlobals()
  if (originalFonts) Object.defineProperty(document, 'fonts', originalFonts)
  else Reflect.deleteProperty(document, 'fonts')
})

describe('deferred terminal focus', () => {
  it('focuses the terminal input after Find closes before terminal readiness', async () => {
    const { resolveFonts, fontsReady } = mountWithPendingFonts()

    expect(screen.queryByRole('textbox', { name: 'Terminal input' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Open terminal search' }))
    const findInput = screen.getByLabelText('Find in terminal')
    expect(document.activeElement).toBe(findInput)

    fireEvent.keyDown(findInput, { key: 'Escape' })
    expect(screen.queryByLabelText('Find in terminal')).toBeNull()

    await releaseFonts(resolveFonts, fontsReady)
    const terminalInput = screen.getByRole('textbox', { name: 'Terminal input' })
    expect(document.activeElement).toBe(terminalInput)
  })

  it('does not revive a cancelled Find focus intent after external focus moves to body', async () => {
    const { resolveFonts, fontsReady } = mountWithPendingFonts(<PaneWithExternalControl />)
    const external = screen.getByRole('button', { name: 'External control' })

    fireEvent.click(screen.getByRole('button', { name: 'Open terminal search' }))
    const findInput = screen.getByLabelText('Find in terminal')
    fireEvent.keyDown(findInput, { key: 'Escape' })

    act(() => {
      external.focus()
      external.blur()
    })
    expect(document.activeElement).toBe(document.body)

    await releaseFonts(resolveFonts, fontsReady)
    screen.getByRole('textbox', { name: 'Terminal input' })
    expect(document.activeElement).toBe(document.body)
  })
})
