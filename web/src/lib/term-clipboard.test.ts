import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Terminal } from '@xterm/xterm'
import { toast } from 'sonner'
import {
  clipboardKeys,
  copySelection,
  pasteClipboard,
  registerClipboardImages,
  type ClipboardImageHandler,
} from './term-clipboard'

vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

type KeyHandler = (ev: KeyboardEvent) => boolean

function mount(selection = '') {
  const paste = vi.fn()
  const focus = vi.fn()
  const input = document.createElement('textarea')
  document.body.append(input)
  const term = {
    hasSelection: () => selection.length > 0,
    getSelection: () => selection,
    paste,
    focus,
    textarea: input,
  }
  const handler: KeyHandler = clipboardKeys(term as unknown as Terminal)
  return { handler, paste, focus, input, term: term as unknown as Terminal }
}

function key(opts: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...opts })
}

function pasteEvent(input: HTMLTextAreaElement, clipboardData: DataTransfer): ClipboardEvent {
  const event = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent
  Object.defineProperty(event, 'clipboardData', { value: clipboardData })
  input.dispatchEvent(event)
  return event
}

function clipboardData(files: File[] = [], items: DataTransferItem[] = []): DataTransfer {
  return { files, items } as unknown as DataTransfer
}

describe('terminal clipboard keys', () => {
  afterEach(() => {
    vi.clearAllMocks()
    vi.unstubAllGlobals()
    Object.defineProperty(document, 'execCommand', { value: undefined, configurable: true })
    document.body.replaceChildren()
  })

  it('copies the selection on ctrl+shift+c and swallows the key', () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const { handler } = mount('picked text')
    const event = key({ code: 'KeyC', ctrlKey: true, shiftKey: true })
    const preventDefault = vi.spyOn(event, 'preventDefault')

    expect(handler(event)).toBe(false)
    expect(preventDefault).toHaveBeenCalledOnce()
    expect(writeText).toHaveBeenCalledWith('picked text')
  })

  it('copies on a plain ctrl+c only when a selection exists', () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const withSelection = mount('selected')
    const noSelection = mount('')
    const selectedEvent = key({ code: 'KeyC', ctrlKey: true })
    const emptyEvent = key({ code: 'KeyC', ctrlKey: true })
    const selectedPreventDefault = vi.spyOn(selectedEvent, 'preventDefault')
    const emptyPreventDefault = vi.spyOn(emptyEvent, 'preventDefault')

    expect(withSelection.handler(selectedEvent)).toBe(false)
    expect(selectedPreventDefault).toHaveBeenCalledOnce()
    expect(writeText).toHaveBeenCalledWith('selected')
    expect(noSelection.handler(emptyEvent)).toBe(true)
    expect(emptyPreventDefault).not.toHaveBeenCalled()
  })

  it('leaves ctrl+shift+v native so Windows paste survives missing async clipboard APIs', () => {
    const { handler, paste } = mount('')
    const event = key({ code: 'KeyV', ctrlKey: true, shiftKey: true })
    const preventDefault = vi.spyOn(event, 'preventDefault')

    expect(handler(event)).toBe(true)
    expect(preventDefault).not.toHaveBeenCalled()
    expect(paste).not.toHaveBeenCalled()
  })
  it('does not let a denied readText call cancel native ctrl+shift+v', () => {
    const readText = vi.fn(async () => {
      throw new DOMException('permission denied', 'NotAllowedError')
    })
    vi.stubGlobal('navigator', { ...navigator, clipboard: { readText } })
    const { handler } = mount('')
    const event = key({ code: 'KeyV', ctrlKey: true, shiftKey: true })
    const preventDefault = vi.spyOn(event, 'preventDefault')

    expect(handler(event)).toBe(true)
    expect(preventDefault).not.toHaveBeenCalled()
    expect(readText).not.toHaveBeenCalled()
  })

  it('does not claim macOS command copy and paste or AltGr combinations', () => {
    const { handler } = mount('picked text')

    expect(handler(key({ code: 'KeyC', metaKey: true }))).toBe(true)
    expect(handler(key({ code: 'KeyV', metaKey: true }))).toBe(true)
    expect(handler(key({ code: 'KeyC', ctrlKey: true, altKey: true, shiftKey: true }))).toBe(true)
    expect(handler(key({ code: 'KeyV', ctrlKey: true, altKey: true, shiftKey: true }))).toBe(true)
  })

  it('delivers a native image paste once and leaves text-only paste native', async () => {
    const { input, term } = mount()
    const handler = vi.fn(async () => {})
    const unregister = registerClipboardImages(term, handler)
    const image = new File(['png'], 'shot.png', { type: 'image/png' })
    const imageEvent = pasteEvent(input, clipboardData([image]))
    const textEvent = pasteEvent(
      input,
      clipboardData([], [
        { kind: 'string', type: 'text/plain', getAsFile: () => null } as unknown as DataTransferItem,
      ]),
    )

    await vi.waitFor(() => expect(handler).toHaveBeenCalledWith([image]))
    expect(handler).toHaveBeenCalledOnce()
    expect(imageEvent.defaultPrevented).toBe(true)
    expect(textEvent.defaultPrevented).toBe(false)
    unregister()
  })

  it('removes the native image listener when unregistered', () => {
    const { input, term } = mount()
    const handler = vi.fn(async () => {})
    const unregister = registerClipboardImages(term, handler)
    unregister()

    const event = pasteEvent(
      input,
      clipboardData([new File(['png'], 'shot.png', { type: 'image/png' })]),
    )

    expect(event.defaultPrevented).toBe(false)
    expect(handler).not.toHaveBeenCalled()
  })
  it('does not read arbitrary path-only clipboard text as a local image', () => {
    const { input, term } = mount()
    const handler = vi.fn(async () => {})
    registerClipboardImages(term, handler)
    const event = pasteEvent(
      input,
      clipboardData([], [
        { kind: 'string', type: 'text/plain', getAsFile: () => null } as unknown as DataTransferItem,
      ]),
    )

    expect(event.defaultPrevented).toBe(false)
    expect(handler).not.toHaveBeenCalled()
  })

  it('uses image clipboard data once before toolbar text fallback', async () => {
    const { term, paste } = mount()
    const image = new Blob(['png'], { type: 'image/png' })
    const read = vi.fn(async () => [
      { types: ['image/png'], getType: vi.fn(async () => image) },
    ])
    const readText = vi.fn(async () => 'not inserted')
    const handler = vi.fn<ClipboardImageHandler>(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { read, readText } })

    await pasteClipboard(term, handler)

    expect(read).toHaveBeenCalledOnce()
    expect(handler).toHaveBeenCalledOnce()
    const receivedFiles = handler.mock.calls[0]?.[0]
    expect(receivedFiles?.[0]).toMatchObject({ type: 'image/png' })
    expect(readText).not.toHaveBeenCalled()
    expect(paste).not.toHaveBeenCalled()
  })
  it('drops a toolbar image read that finishes after its terminal registration is gone', async () => {
    const { term } = mount()
    const handler = vi.fn(async () => {})
    const unregister = registerClipboardImages(term, handler)
    type ImageItem = { types: string[]; getType: (type: string) => Promise<Blob> }
    let resolveRead: (items: ImageItem[]) => void = () => {}
    const read = vi.fn(
      () =>
        new Promise<ImageItem[]>((resolve) => {
          resolveRead = resolve
        }),
    )
    vi.stubGlobal('navigator', { ...navigator, clipboard: { read } })

    const pending = pasteClipboard(term)
    unregister()
    resolveRead([{ types: ['image/png'], getType: async () => new Blob(['png']) }])
    await pending

    expect(handler).not.toHaveBeenCalled()
  })

  it('falls back to toolbar text when image clipboard reads are unavailable', async () => {
    const { term, paste, focus } = mount()
    const readText = vi.fn(async () => 'from the clipboard')
    vi.stubGlobal('navigator', { ...navigator, clipboard: { readText } })

    await pasteClipboard(term)

    expect(readText).toHaveBeenCalledOnce()
    expect(paste).toHaveBeenCalledWith('from the clipboard')
    expect(focus).toHaveBeenCalled()
  })

  it('preserves the platform clipboard denial detail', async () => {
    const { term } = mount()
    const failure = new DOMException('permission denied', 'NotAllowedError')
    const readText = vi.fn(async () => {
      throw failure
    })
    vi.stubGlobal('navigator', { ...navigator, clipboard: { readText } })

    await pasteClipboard(term)

    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining(failure.message))
  })

  it('restores terminal focus after the execCommand copy fallback', async () => {
    const { term, input } = mount('selected text')
    input.focus()
    const execCommand = vi.fn(() => true)
    Object.defineProperty(document, 'execCommand', { value: execCommand, configurable: true })
    vi.stubGlobal('navigator', { ...navigator, clipboard: undefined })

    await expect(copySelection(term)).resolves.toBe(true)

    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(document.activeElement).toBe(input)
  })
})
