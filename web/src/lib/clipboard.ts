import { toast } from 'sonner'

/**
 * Put `text` on the clipboard. jsdom, plain-http origins and older engines
 * have no navigator.clipboard, so the fallback selects `fallback`'s contents
 * and leaves them for a manual copy rather than failing silently.
 */
export async function copyText(text: string, fallback: HTMLElement | null): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    toast.success('Copied')
  } catch {
    if (!fallback) return
    const range = document.createRange()
    range.selectNodeContents(fallback)
    const selection = window.getSelection()
    selection?.removeAllRanges()
    selection?.addRange(range)
    toast.info('Selected. Press Ctrl+C to copy.')
  }
}
