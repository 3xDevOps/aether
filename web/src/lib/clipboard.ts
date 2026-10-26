import { toast } from 'sonner'
import { message } from '@/lib/format'

/**
 * Put `text` on the clipboard. jsdom, plain-http origins and older engines
 * have no navigator.clipboard, so the fallback selects `fallback`'s contents
 * and leaves them for a manual copy rather than failing silently. Without a
 * fallback element the error toast carries the text instead. A `fallback`
 * function runs only on that path, so it can first show text it clips.
 * `quiet` skips the success toast for a caller that confirms in place; it
 * resolves true only when the text reached the clipboard.
 */
export async function copyText(text: string, fallback: HTMLElement | null | (() => HTMLElement | null), { quiet = false } = {}): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    if (!quiet) toast.success('Copied')
    return true
  } catch (err) {
    const element = typeof fallback === 'function' ? fallback() : fallback
    if (!element) {
      toast.error(`Could not copy: ${message(err)}`, { description: text })
      return false
    }
    const range = document.createRange()
    range.selectNodeContents(element)
    const selection = window.getSelection()
    selection?.removeAllRanges()
    selection?.addRange(range)
    toast.info('Selected. Press Ctrl+C to copy.')
    return false
  }
}
