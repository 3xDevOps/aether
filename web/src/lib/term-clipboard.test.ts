import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Terminal } from '@xterm/xterm'
import { attachClipboardKeys } from './term-clipboard'

type KeyHandler = (ev: KeyboardEvent) => boolean

function mount(selection: string) {
  let handler: KeyHandler = () => true
  const paste = vi.fn()
  const term = {
    hasSelection: () => selection.length > 0,
    getSelection: () => selection,
    paste,
    attachCustomKeyEventHandler: (h: KeyHandler) => {
      handler = h
    },
  }
  attachClipboardKeys(term as unknown as Terminal)
  return { handler, paste }
}

function key(opts: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...opts })
}

describe('terminal clipboard keys', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    Object.defineProperty(document, 'execCommand', { value: undefined, configurable: true })
  })

  it('copies the selection on ctrl+shift+c and swallows the key', () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const { handler } = mount('picked text')

    expect(handler(key({ code: 'KeyC', ctrlKey: true, shiftKey: true }))).toBe(false)
    // The writeText call happens synchronously inside the handler's promise.
    expect(writeText).toHaveBeenCalledWith('picked text')
  })

  it('copies on a plain ctrl+c only when a selection exists', () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const withSelection = mount('selected')
    const noSelection = mount('')

    // With a selection the user means copy; without one it is the interrupt
    // and must reach the terminal untouched.
    expect(withSelection.handler(key({ code: 'KeyC', ctrlKey: true }))).toBe(false)
    expect(writeText).toHaveBeenCalledWith('selected')
    expect(noSelection.handler(key({ code: 'KeyC', ctrlKey: true }))).toBe(true)
  })

  it('pastes clipboard text on ctrl+shift+v', () => {
    const readText = vi.fn(async () => 'from the clipboard')
    vi.stubGlobal('navigator', { ...navigator, clipboard: { readText } })
    const { handler, paste } = mount('')

    expect(handler(key({ code: 'KeyV', ctrlKey: true, shiftKey: true }))).toBe(false)
    expect(readText).toHaveBeenCalled()
    // The read resolves in a microtask; flush it before asserting.
    return vi.waitFor(() => expect(paste).toHaveBeenCalledWith('from the clipboard'))
  })

  it('falls back to execCommand copy when the clipboard api is missing', () => {
    // jsdom ships no navigator.clipboard - the plain-http-origin reality the
    // fallback exists for.
    const execCommand = vi.fn(() => true)
    Object.defineProperty(document, 'execCommand', { value: execCommand, configurable: true })
    const { handler } = mount('selected text')

    expect(handler(key({ code: 'KeyC', ctrlKey: true, shiftKey: true }))).toBe(false)
    expect(execCommand).toHaveBeenCalledWith('copy')
  })

  it('paste is a no-op without a clipboard api', () => {
    const { handler, paste } = mount('')

    expect(handler(key({ code: 'KeyV', ctrlKey: true, shiftKey: true }))).toBe(false)
    expect(paste).not.toHaveBeenCalled()
  })
})