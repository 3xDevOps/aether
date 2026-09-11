import { fireEvent, render, screen } from '@testing-library/react'
import { Terminal } from '@xterm/xterm'
import { TerminalKeys } from '@/components/terminal-keys'
import type { XtermController } from '@/components/xterm-host'

function controller(over: Partial<XtermController> = {}): XtermController {
  return {
    hostRef: () => {},
    terminal: null,
    ready: false,
    search: null,
    findOpen: false,
    setFindOpen: () => {},
    focusTerminal: () => {},
    ctrlArmed: false,
    armCtrl: () => {},
    ...over,
  }
}

describe('the terminal key bar', () => {
  it('sends the bytes a keyboard would for the keys a phone has not got', () => {
    const terminal = new Terminal()
    const sent: string[] = []
    terminal.onData((data) => sent.push(data))
    render(<TerminalKeys controller={controller({ terminal })} />)

    for (const key of ['Esc', 'Tab', 'Up', 'Down', 'Left', 'Right', 'Enter', 'Ctrl+C']) {
      fireEvent.click(screen.getByRole('button', { name: key }))
    }

    expect(sent).toEqual([
      '\x1b',
      '\t',
      '\x1b[A',
      '\x1b[B',
      '\x1b[D',
      '\x1b[C',
      '\r',
      '\x03',
    ])
    terminal.dispose()
  })

  it('reports the Ctrl modifier as pressed and asks the host to arm it', () => {
    const armCtrl = vi.fn()
    const view = render(<TerminalKeys controller={controller({ armCtrl })} />)
    fireEvent.click(screen.getByRole('button', { name: 'Ctrl' }))
    expect(armCtrl).toHaveBeenCalledWith(true)

    view.rerender(<TerminalKeys controller={controller({ armCtrl, ctrlArmed: true })} />)
    expect(
      screen.getByRole('button', { name: 'Ctrl' }).getAttribute('aria-pressed'),
    ).toBe('true')
    fireEvent.click(screen.getByRole('button', { name: 'Ctrl' }))
    expect(armCtrl).toHaveBeenLastCalledWith(false)
  })

  it('keeps the keyboard up by refusing the focus a tap would take', () => {
    render(<TerminalKeys controller={controller()} />)
    const press = fireEvent.pointerDown(screen.getByRole('button', { name: 'Esc' }))
    // fireEvent returns false when a handler called preventDefault.
    expect(press).toBe(false)
  })
})
