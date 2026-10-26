import * as React from 'react'
import { cn } from '@/lib/utils'

const fade = '22px'

/** A row that scrolls sideways with no scrollbar and fades whichever edge hides more of it. */
export function FadeStrip({ className, children }: { className?: string; children: React.ReactNode }) {
  const ref = React.useRef<HTMLDivElement>(null)
  const [edges, setEdges] = React.useState({ left: false, right: false })

  React.useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const measure = () => {
      const next = { left: el.scrollLeft > 1, right: el.scrollLeft < el.scrollWidth - el.clientWidth - 1 }
      setEdges((prev) => (prev.left === next.left && prev.right === next.right ? prev : next))
    }
    measure()
    el.addEventListener('scroll', measure, { passive: true })
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    if (el.firstElementChild) observer.observe(el.firstElementChild)
    return () => {
      el.removeEventListener('scroll', measure)
      observer.disconnect()
    }
  }, [])

  // A mask rather than a painted gradient, so the fade matches any surface.
  const mask = `linear-gradient(to right, ${edges.left ? 'transparent' : '#000'}, #000 ${fade}, #000 calc(100% - ${fade}), ${edges.right ? 'transparent' : '#000'})`
  return (
    <div
      ref={ref}
      data-slot="fade-strip"
      style={edges.left || edges.right ? { maskImage: mask } : undefined}
      className={cn('min-w-0 flex-1 overflow-x-auto overscroll-x-contain [scrollbar-width:none] [&::-webkit-scrollbar]:hidden', className)}
    >
      {children}
    </div>
  )
}
