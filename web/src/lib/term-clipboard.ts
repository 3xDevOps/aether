import type { Terminal } from '@xterm/xterm'

/**
 * Terminal clipboard wiring, shared by the run terminal and the workspace
 * shell pane: Ctrl+Shift+C copies the selection, Ctrl+Shift+V pastes, and a
 * plain Ctrl+C copies instead of sending SIGINT when a selection exists.
 * Plain Ctrl+V stays native - the browser's paste event reaches xterm's
 * textarea on its own.
 *
 * Shortcuts key off physical key codes so a remapped keyboard layout cannot
 * move them.
 */

/** Install the clipboard shortcuts on a terminal. Call after open. */
export function attachClipboardKeys(term: Terminal): void {
  term.attachCustomKeyEventHandler((ev) => {
    if (ev.type !== 'keydown') return true
    if (ev.ctrlKey && ev.shiftKey && ev.code === 'KeyC') {
      void copySelection(term)
      return false
    }
    if (ev.ctrlKey && ev.shiftKey && ev.code === 'KeyV') {
      void pasteClipboard(term)
      return false
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
      void copySelection(term)
      return false
    }
    return true
  })
}

/** Copy the terminal's selection, reporting whether it reached a clipboard. */
export async function copySelection(term: Terminal): Promise<boolean> {
  const text = term.getSelection()
  if (!text) return false
  if (navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Denied at call time; the execCommand fallback is the page's only
      // other route to the clipboard.
    }
  }
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
  return copied
}

/** Paste the clipboard into the terminal. The clipboard API is a terminal's
 * only paste route, so a missing API or a denial is a silent no-op. */
export async function pasteClipboard(term: Terminal): Promise<void> {
  const clipboard = navigator.clipboard
  if (!clipboard?.readText) return
  try {
    const text = await clipboard.readText()
    if (text) term.paste(text)
  } catch {
    // Permission denied or unsupported engine: nothing to paste.
  }
}