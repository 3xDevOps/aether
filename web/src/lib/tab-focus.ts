import { useEffect } from 'react'

// Browsers match :focus-visible after any key press followed by a scripted
// focus move (Enter to send, a shortcut, a closing dialog), outlining controls
// nobody tabbed to. Focus outlines also need `data-tabbing` on the root, which
// Tab sets and the next pointer press clears; see `focus-visible` in index.css.
export function useTabFocus() {
  useEffect(() => {
    const root = document.documentElement
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Tab') root.dataset.tabbing = ''
    }
    const pointer = () => {
      delete root.dataset.tabbing
    }
    window.addEventListener('keydown', key)
    window.addEventListener('pointerdown', pointer, true)
    return () => {
      window.removeEventListener('keydown', key)
      window.removeEventListener('pointerdown', pointer, true)
      delete root.dataset.tabbing
    }
  }, [])
}
