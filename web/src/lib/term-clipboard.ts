import type { Terminal } from '@xterm/xterm'
import { toast } from 'sonner'

/**
 * Terminal clipboard wiring, shared by the run terminal and the workspace
 * shell pane. Plain paste is deliberately left to xterm/the browser: unlike
 * an async clipboard read, the native paste event continues to work on
 * Windows when clipboard-read permission is absent.
 *
 * Image clipboard data is the one exception. Browsers do not make image
 * files available to xterm, so a registered image handler claims native
 * image paste events and receives the actual File objects. Text-only paste
 * events are never claimed.
 */

export type ClipboardImageHandler = (files: File[]) => Promise<void>

type TerminalInput = HTMLTextAreaElement & {
  addEventListener: HTMLTextAreaElement['addEventListener']
}

type Registration = {
  handler: ClipboardImageHandler
  input: TerminalInput | null
  onPaste: (event: Event) => void
}

const registrations = new WeakMap<Terminal, Registration>()

const IMAGE_TYPES: Record<string, true> = {
  'image/png': true,
  'image/jpeg': true,
  'image/gif': true,
  'image/webp': true,
}

function terminalInput(term: Terminal): TerminalInput | null {
  const input = (term as Terminal & { textarea?: HTMLTextAreaElement | null }).textarea
  return input ?? null
}

function isImageType(type: string): boolean {
  return IMAGE_TYPES[type.toLowerCase()] === true
}

function imageFiles(data: DataTransfer | null): File[] {
  if (!data) return []
  const files = Array.from(data.files ?? []).filter((file) => isImageType(file.type))
  if (files.length) return files

  // Some Chromium clipboard providers expose the image only as a DataTransfer
  // item, not through `files`.
  return Array.from(data.items ?? [])
    .filter((item) => item.kind === 'file' && isImageType(item.type))
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null)
}

function errorMessage(error: unknown): string {
  if (typeof error === 'object' && error !== null && 'message' in error) {
    const message = error.message
    if (typeof message === 'string' && message) return message
  }
  return 'clipboard access was denied'
}

function reportPasteFailure(detail: string): void {
  toast.error(`Paste unavailable: ${detail}`)
}

async function dispatchImages(handler: ClipboardImageHandler, files: File[]): Promise<void> {
  try {
    await handler(files)
  } catch (error) {
    toast.error(`Pasting image failed: ${errorMessage(error)}`)
  }
}

/**
 * Register the image half of terminal clipboard handling.
 *
 * The listener is capture-phase so it runs before xterm's own paste listener.
 * It claims only native paste events containing supported image files; plain
 * text remains native. A second registration for the same terminal replaces
 * the first one, which prevents stale run/target handlers from surviving a
 * terminal reuse.
 */
export function registerClipboardImages(
  term: Terminal,
  handler: ClipboardImageHandler,
): () => void {
  const previous = registrations.get(term)
  previous?.input?.removeEventListener('paste', previous.onPaste, true)
  const input = terminalInput(term)
  const registration: Registration = {
    handler,
    input,
    onPaste: () => {},
  }
  registration.onPaste = (rawEvent) => {
    const event = rawEvent as ClipboardEvent
    const files = imageFiles(event.clipboardData)
    if (!files.length) return

    event.preventDefault()
    event.stopImmediatePropagation()
    void dispatchImages(registration.handler, files)
  }
  input?.addEventListener('paste', registration.onPaste, true)
  registrations.set(term, registration)

  let active = true
  return () => {
    if (!active) return
    active = false
    input?.removeEventListener('paste', registration.onPaste, true)
    if (registrations.get(term) === registration) registrations.delete(term)
  }
}

/**
 * The clipboard half of xterm's key handling, returned as a predicate rather
 * than installed directly: xterm keeps one custom key handler, and the host
 * composes this with the zoom and find shortcuts.
 */
export function clipboardKeys(term: Terminal): (ev: KeyboardEvent) => boolean {
  return (ev) => {
    if (ev.type !== 'keydown') return true
    if (
      ev.ctrlKey &&
      ev.shiftKey &&
      !ev.altKey &&
      !ev.metaKey &&
      ev.code === 'KeyC'
    ) {
      ev.preventDefault()
      void copySelection(term)
      return false
    }
    if (
      ev.ctrlKey &&
      ev.shiftKey &&
      !ev.altKey &&
      !ev.metaKey &&
      ev.code === 'KeyV'
    ) {
      // Keep the native event alive. This is the critical Windows path:
      // read()/readText() may be denied or absent, while xterm's paste event
      // still has access to the clipboard. A registered capture listener
      // claims image events; text remains native and cannot be delivered twice.
      return true
    }
    if (
      ev.ctrlKey &&
      !ev.shiftKey &&
      !ev.altKey &&
      !ev.metaKey &&
      ev.code === 'KeyC' &&
      term.hasSelection()
    ) {
      // A selection means the user wants it copied. Without one, Ctrl+C is
      // the interrupt and must reach the terminal untouched.
      ev.preventDefault()
      void copySelection(term)
      return false
    }
    return true
  }
}

/** Copy the terminal's selection, reporting whether it reached a clipboard. */
export async function copySelection(term: Terminal): Promise<boolean> {
  return writeText(term.getSelection())
}

/**
 * Copy the rows on screen. A drag selection is what copy normally needs, and
 * touch has no drag over a terminal, so this is how a phone gets the output
 * it is looking at out of the terminal.
 */
export async function copyScreen(term: Terminal): Promise<boolean> {
  const buffer = term.buffer.active
  const rows: string[] = []
  for (let row = 0; row < term.rows; row++) {
    rows.push(buffer.getLine(buffer.viewportY + row)?.translateToString(true) ?? '')
  }
  while (rows.length > 0 && rows[rows.length - 1] === '') rows.pop()
  return writeText(rows.join('\n'))
}

async function writeText(text: string): Promise<boolean> {
  if (!text) return false
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Denied at call time; the execCommand fallback is the page's only
      // other route to the clipboard.
    }
  }

  const previousFocus = document.activeElement
  const helper = document.createElement('textarea')
  helper.value = text
  helper.setAttribute('readonly', '')
  helper.style.position = 'fixed'
  helper.style.opacity = '0'
  document.body.appendChild(helper)
  helper.select()
  let copied = false
  try {
    copied = document.execCommand('copy')
  } catch {
    copied = false
  }
  helper.remove()
  if (previousFocus instanceof HTMLElement && previousFocus.isConnected) previousFocus.focus()
  if (!copied) toast.error('Copy unavailable: allow clipboard access or use your browser copy command')
  return copied
}

type ClipboardItemLike = {
  types: readonly string[]
  getType: (type: string) => Promise<Blob>
}

function fileExtension(type: string): string {
  return type === 'image/jpeg' ? 'jpg' : type.slice('image/'.length)
}

async function filesFromItems(items: readonly ClipboardItemLike[]): Promise<File[]> {
  const files: File[] = []
  for (const item of items) {
    const type = item.types.find(isImageType)
    if (!type) continue
    const blob = await item.getType(type)
    files.push(new File([blob], `clipboard.${fileExtension(type)}`, { type }))
  }
  return files
}

/**
 * Paste from the toolbar (or another explicit UI action). Images are read
 * first when the image-capable API exists; text is the fallback. The optional
 * handler is useful while a UI is transitioning between targets, while a
 * registered handler is used by default.
 */
export async function pasteClipboard(
  term: Terminal,
  imageHandler?: ClipboardImageHandler,
): Promise<void> {
  const handler = imageHandler ?? registrations.get(term)?.handler
  const registration = registrations.get(term)
  term.focus()
  const clipboard = navigator.clipboard
  if (!clipboard) {
    reportPasteFailure('use Ctrl+V in the terminal or allow clipboard access')
    return
  }

  let imageReadError: unknown = null
  if (clipboard.read) {
    try {
      const files = await filesFromItems(await clipboard.read())
      if (files.length) {
        if (!handler) {
          reportPasteFailure('no image handler is registered for this terminal')
          return
        }
        if (registrations.get(term) !== registration) return
        await dispatchImages(handler, files)
        term.focus()
        return
      }
    } catch (error) {
      imageReadError = error
    }
  }

  if (clipboard.readText) {
    try {
      const text = await clipboard.readText()
      if (text) term.paste(text)
      term.focus()
      return
    } catch (error) {
      reportPasteFailure(
        imageReadError
          ? `${errorMessage(imageReadError)}; use Ctrl+V in the terminal or allow clipboard access`
          : `${errorMessage(error)}; use Ctrl+V in the terminal or allow clipboard access`,
      )
      term.focus()
      return
    }
  }

  reportPasteFailure(
    imageReadError
      ? `${errorMessage(imageReadError)}; use Ctrl+V in the terminal or allow clipboard access`
      : 'this browser does not expose clipboard read; use Ctrl+V in the terminal',
  )
  term.focus()
}
