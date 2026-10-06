import { useEffect } from 'react'

// iOS Safari ignores `interactive-widget=resizes-content`: the soft keyboard shrinks only the visual
// viewport, so a sheet pinned to the layout viewport's bottom sits behind it. `--keyboard-inset` is the
// height the keyboard covers, 0 where the browser resizes the layout viewport itself.
export function useKeyboardInset() {
  useEffect(() => {
    const viewport = window.visualViewport
    if (!viewport) return
    const root = document.documentElement
    const update = () => {
      const inset = Math.max(0, window.innerHeight - viewport.height - viewport.offsetTop)
      root.style.setProperty('--keyboard-inset', `${Math.round(inset)}px`)
    }
    update()
    viewport.addEventListener('resize', update)
    viewport.addEventListener('scroll', update)
    return () => {
      viewport.removeEventListener('resize', update)
      viewport.removeEventListener('scroll', update)
      root.style.removeProperty('--keyboard-inset')
    }
  }, [])
}
