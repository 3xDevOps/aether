import { useLayoutEffect, useState } from 'react'
import type { PatchFile } from '@/routes/diff/parse'

/** Past this many content lines or files only the files near the screen are mounted. */
const largePatch = 1500
const largeFileCount = 100

export function isLargeDiff(files: PatchFile[]): boolean {
  return files.length > largeFileCount || files.reduce((total, file) => total + file.contentLines, 0) > largePatch
}

/** Where `element` starts in the scrolled `content`, kept current as anything above it changes size. */
export function useOffsetTop(content: HTMLElement | null, element: HTMLElement | null): number {
  const [top, setTop] = useState(0)
  useLayoutEffect(() => {
    if (!content || !element) return
    const measure = () => setTop(element.offsetTop)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(content)
    observer.observe(element)
    return () => observer.disconnect()
  }, [content, element])
  return top
}

/** Unlike scrollIntoView, never scrolls `scroller`'s ancestors, which would push the session header out of view. */
export function scrollWithin(scroller: HTMLElement | null, element: HTMLElement | null, offset: number) {
  if (!scroller || !element) return
  scroller.scrollTo({ top: scroller.scrollTop + element.getBoundingClientRect().top - scroller.getBoundingClientRect().top - offset })
}
