import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'

export function ClampedText({ text }: { text: string }) {
  const ref = useRef<HTMLParagraphElement>(null)
  const [open, setOpen] = useState(false)
  const [clipped, setClipped] = useState(false)
  useEffect(() => {
    const element = ref.current
    if (!element || open) return
    const measure = () => setClipped(element.scrollHeight > element.clientHeight + 1)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [text, open])
  return (
    <div className="min-w-0">
      <p ref={ref} className={`whitespace-pre-wrap break-words ${open ? '' : 'line-clamp-3'}`}>{text}</p>
      {(clipped || open) && (
        <Button variant="link" size="sm" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
          {open ? 'Show less' : 'Show more'}
        </Button>
      )}
    </div>
  )
}
