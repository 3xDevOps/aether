import { fireEvent, render, screen } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import { TerminalKeys } from '@/components/terminal-keys'
import { TerminalPane } from '@/components/terminal-pane'
import { noModifiers } from '@/components/terminal-modifiers'
import type { XtermController } from '@/components/xterm-host'

function controller(over: Partial<XtermController> = {}): XtermController {
  return {
    hostRef: () => {},
    terminal: null,
    ready: false,
    geometry: () => ({ cols: 80, rows: 24 }),
    setGeometry: () => {},
    search: null,
    findOpen: false,
    setFindOpen: () => {},
    focusTerminal: () => {},
    modifiers: noModifiers,
    tapModifier: () => {},
    clearModifiers: () => {},
    ...over,
  }
}

describe('the terminal key bar', () => {
  it('releases armed modifiers when the terminal stops taking input', () => {
    const clearModifiers = vi.fn()
    const props = { controller: controller({ clearModifiers, modifiers: { ctrl: 'once', alt: 'off' } }), writable: true }
    const view = render(<TerminalPane {...props} />)
    expect(clearModifiers).not.toHaveBeenCalled()

    // Losing write takes the bar away. A modifier left armed behind it
    // would turn the first character of the next turn at the keyboard
    // into a control code nobody pressed.
    view.rerender(<TerminalPane {...props} writable={false} />)
    expect(clearModifiers).toHaveBeenCalled()
  })

  it('keeps xterm and the mobile controls read-only when writable is lost', () => {
    const original = window.matchMedia
    window.matchMedia = vi.fn((query: string) => ({
      matches: query === '(pointer: coarse)',
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })) as typeof window.matchMedia
    try {
      const terminal = new Terminal()
      const clearModifiers = vi.fn()
      const props = { controller: controller({ terminal, clearModifiers }), writable: true }
      const view = render(<TerminalPane {...props} />)

      expect(terminal.options.disableStdin).toBe(false)
      expect(screen.getByRole('toolbar', { name: 'Terminal keys' })).toBeDefined()
      view.rerender(<TerminalPane {...props} writable={false} />)

      expect(terminal.options.disableStdin).toBe(true)
      expect(clearModifiers).toHaveBeenCalled()
      expect(screen.queryByRole('toolbar', { name: 'Terminal keys' })).toBeNull()

      view.rerender(<TerminalPane {...props} writable />)
      expect(terminal.options.disableStdin).toBe(false)
      expect(screen.getByRole('toolbar', { name: 'Terminal keys' })).toBeDefined()
      terminal.dispose()
    } finally {
      window.matchMedia = original
    }
  })

  it('sends the bytes a keyboard would for the keys a phone has not got', () => {
    const terminal = new Terminal()
    const sent: string[] = []
    terminal.onData((data) => sent.push(data))
    render(<TerminalKeys fontSize={12} controller={controller({ terminal })} />)

    for (const key of ['Esc', 'Tab', 'Up', 'Down', 'Left', 'Right', 'Enter']) {
      fireEvent.click(screen.getByRole('button', { name: key }))
    }
    fireEvent.click(screen.getByRole('button', { name: 'More keys' }))
    fireEvent.click(screen.getByRole('button', { name: 'PgUp' }))
    fireEvent.click(screen.getByRole('button', { name: 'More keys' }))
    fireEvent.click(screen.getByRole('button', { name: 'F5' }))

    expect(sent).toEqual(['\x1b', '\t', '\x1b[A', '\x1b[B', '\x1b[D', '\x1b[C', '\r', '\x1b[5~', '\x1b[15~'])
    terminal.dispose()
  })

  it('shows a modifier as pressed once or locked, and asks the host to step it', () => {
    const tapModifier = vi.fn()
    const view = render(<TerminalKeys fontSize={12} controller={controller({ tapModifier })} />)
    fireEvent.click(screen.getByRole('button', { name: 'Alt' }))
    expect(tapModifier).toHaveBeenCalledWith('alt')

    view.rerender(<TerminalKeys fontSize={12} controller={controller({ tapModifier, modifiers: { ctrl: 'locked', alt: 'once' } })} />)
    expect(screen.getByRole('button', { name: 'Alt' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('button', { name: 'Ctrl locked' }).getAttribute('aria-pressed')).toBe('true')
  })

  it('keeps the keyboard up by refusing the focus a tap would take', () => {
    render(<TerminalKeys fontSize={12} controller={controller()} />)
    const press = fireEvent.pointerDown(screen.getByRole('button', { name: 'Esc' }))
    // fireEvent returns false when a handler called preventDefault.
    expect(press).toBe(false)
  })
})
