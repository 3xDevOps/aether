import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { Terminal } from '@xterm/xterm'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import {
  TerminalImageAction,
  useTerminalImage,
  validateTerminalImage,
} from './terminal-image'

function imageFile(name = 'screen.png', type = 'image/png', size = 4): File {
  return new File([new Uint8Array(size)], name, { type })
}

function Probe({
  terminal,
  imageTargetKey = 'main',
  imageUploadEnabled = true,
}: {
  terminal: Terminal
  imageTargetKey?: string
  imageUploadEnabled?: boolean
}) {
  const image = useTerminalImage({
    terminal,
    imageTargetKey,
    imageUploadEnabled,
    focusTerminal: () => {},
  })
  return (
    <>
      <TerminalImageAction controller={image} />
      {image.dialog}
    </>
  )
}

function choose(file: File) {
  const input = document.querySelector('input[type="file"]') as HTMLInputElement | null
  if (!input) throw new Error('file input missing')
  Object.defineProperty(input, 'files', { configurable: true, value: [file] })
  fireEvent.change(input)
}
function nativePaste(input: HTMLTextAreaElement, files: File[]): ClipboardEvent {
  const event = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent
  Object.defineProperty(event, 'clipboardData', {
    value: { files, items: [] } as unknown as DataTransfer,
  })
  act(() => {
    input.dispatchEvent(event)
  })
  return event
}


beforeEach(() => {
  useStore.setState({
    capabilities: {
      gateway: 'remote',
      methods: ['terminal.image'],
      ws: [],
    },
  })
})

afterEach(() => {
  document.querySelectorAll('textarea').forEach((textarea) => textarea.remove())
  vi.restoreAllMocks()
})

describe('terminal image upload', () => {
  it('disables the chooser until the terminal is writable and attached', () => {
    const terminal = {
      textarea: document.createElement('textarea'),
      paste: vi.fn(),
    } as unknown as Terminal
    render(<Probe terminal={terminal} imageUploadEnabled={false} />)
    const button = screen.getByRole('button', { name: 'Upload image to terminal' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
  })

  it('rejects unsupported and oversized files before upload', () => {
    expect(validateTerminalImage(imageFile('x.svg', 'image/svg+xml'))).toContain('PNG')
    expect(validateTerminalImage(imageFile('x.png', 'image/png', 8 * 1024 * 1024 + 1))).toContain('8 MiB')
  })

  it('inserts the quoted remote path without submitting the shell command', async () => {
    const terminal = {
      textarea: document.createElement('textarea'),
      paste: vi.fn(),
    } as unknown as Terminal
    const focus = vi.fn()
    vi.spyOn(api, 'uploadTerminalImage').mockResolvedValue({ path: "/home/member/a'b.png" })
    function FocusProbe() {
      const image = useTerminalImage({ terminal, imageTargetKey: 'main', focusTerminal: focus })
      return (
        <>
          <TerminalImageAction controller={image} />
          {image.dialog}
        </>
      )
    }
    render(<FocusProbe />)
    fireEvent.click(screen.getByRole('button', { name: 'Upload image to terminal' }))
    choose(imageFile())
    fireEvent.click(screen.getByRole('button', { name: 'Upload and insert' }))

    await waitFor(() => expect(terminal.paste).toHaveBeenCalledOnce())
    expect(terminal.paste).toHaveBeenCalledWith("'/home/member/a'\\''b.png'")
    expect(focus).toHaveBeenCalledOnce()
  })

  it('drops a late response after the terminal identity changes', async () => {
    const terminal = {
      textarea: document.createElement('textarea'),
      paste: vi.fn(),
    } as unknown as Terminal
    const pending = Promise.withResolvers<{ path: string }>()
    vi.spyOn(api, 'uploadTerminalImage').mockReturnValue(pending.promise)
    const view = render(<Probe terminal={terminal} />)
    fireEvent.click(screen.getByRole('button', { name: 'Upload image to terminal' }))
    choose(imageFile())
    fireEvent.click(screen.getByRole('button', { name: 'Upload and insert' }))
    view.rerender(<Probe terminal={terminal} imageTargetKey="next" />)
    pending.resolve({ path: '/home/member/late.png' })
    await waitFor(() => expect(terminal.paste).not.toHaveBeenCalled())
  })
  it('opens a native image paste in the current terminal target', async () => {
    const textarea = document.createElement('textarea')
    document.body.append(textarea)
    const terminal = {
      textarea,
      paste: vi.fn(),
    } as unknown as Terminal
    render(<Probe terminal={terminal} />)

    const event = nativePaste(textarea, [imageFile()])

    await waitFor(() => expect(screen.getByText('screen.png')).toBeDefined())
    expect(event.defaultPrevented).toBe(true)
    expect(terminal.paste).not.toHaveBeenCalled()
  })

  it('removes native image handling when the target is disabled or unmounted', () => {
    const textarea = document.createElement('textarea')
    document.body.append(textarea)
    const terminal = {
      textarea,
      paste: vi.fn(),
    } as unknown as Terminal
    const view = render(<Probe terminal={terminal} />)

    view.rerender(<Probe terminal={terminal} imageUploadEnabled={false} />)
    const disabledEvent = nativePaste(textarea, [imageFile()])
    expect(disabledEvent.defaultPrevented).toBe(false)
    expect(screen.queryByText('screen.png')).toBeNull()

    view.unmount()
    const unmountedEvent = nativePaste(textarea, [imageFile()])
    expect(unmountedEvent.defaultPrevented).toBe(false)
  })
})
