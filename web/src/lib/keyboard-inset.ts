import { useEffect, useState } from 'react'

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

// Android's Back and iOS's Done close the keyboard without a blur, so focus alone does not say it is up.
const keyboardHeight = 150

/** An element matching `target` has focus and the visible viewport is a keyboard shorter than the tallest it has been at this width. */
export function useKeyboardUp(target: string): boolean {
  const [up, setUp] = useState(false)
  useEffect(() => {
    const viewport = window.visualViewport
    let full = { width: window.innerWidth, height: window.innerHeight }
    let timer: ReturnType<typeof setTimeout> | undefined
    const update = () => {
      if (window.innerWidth !== full.width) full = { width: window.innerWidth, height: window.innerHeight }
      full.height = Math.max(full.height, window.innerHeight)
      const visible = viewport?.height ?? window.innerHeight
      setUp(full.height - visible > keyboardHeight && document.activeElement?.matches(target) === true)
    }
    // focusout lands on the body before the next focusin, so the check waits for both.
    const later = () => {
      clearTimeout(timer)
      timer = setTimeout(update)
    }
    document.addEventListener('focusin', later)
    document.addEventListener('focusout', later)
    viewport?.addEventListener('resize', update)
    window.addEventListener('resize', update)
    return () => {
      clearTimeout(timer)
      document.removeEventListener('focusin', later)
      document.removeEventListener('focusout', later)
      viewport?.removeEventListener('resize', update)
      window.removeEventListener('resize', update)
    }
  }, [target])
  return up
}
