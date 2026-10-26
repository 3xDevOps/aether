import { useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from 'react'
import { Virtualizer } from 'virtua'

export interface CardEntry {
  key: string
  node: ReactNode
}

export const virtualizeAbove = 60

/** A group's cards, 4px apart; a long group renders only what its scroll shows, and never glides. */
export function CardList({ entries, scrollRef }: { entries: CardEntry[]; scrollRef: RefObject<HTMLElement | null> }) {
  if (entries.length === 0) return null
  if (entries.length > virtualizeAbove) return <VirtualCards entries={entries} scrollRef={scrollRef} />
  return (
    <ul className="flex flex-col">
      {entries.map(({ key, node }) => (
        <li key={key} data-glide-key={key} className="pb-1">
          {node}
        </li>
      ))}
    </ul>
  )
}

function VirtualCards({ entries, scrollRef }: { entries: CardEntry[]; scrollRef: RefObject<HTMLElement | null> }) {
  const top = useRef<HTMLDivElement>(null)
  const [startMargin, setStartMargin] = useState(0)
  const [focused, setFocused] = useState<string | null>(null)
  const focusedIndex = entries.findIndex((entry) => entry.key === focused)
  // In the list every group shares one scroll, so virtua must be told where this group starts in it.
  const measure = () => {
    const scroller = scrollRef.current
    const list = top.current
    if (!scroller || !list) return
    const offset = Math.round(list.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop)
    setStartMargin((current) => (current === offset ? current : offset))
  }
  useLayoutEffect(measure)
  useEffect(() => {
    // On mount React attaches the scroller's ref, an ancestor's, only after this group's layout effects.
    measure()
    const scroller = scrollRef.current
    if (!scroller) return
    // A resize can rewrap the groups above this one without rendering it.
    const observer = new ResizeObserver(() => measure())
    for (const child of scroller.children) observer.observe(child)
    return () => observer.disconnect()
  }, [scrollRef])
  return (
    <div ref={top}>
      <Virtualizer
        as="ul"
        item="li"
        data={entries}
        scrollRef={scrollRef}
        startMargin={startMargin}
        bufferSize={400}
        keepMounted={focusedIndex === -1 ? undefined : [focusedIndex]}
      >
        {({ key, node }) => (
          <div key={key} className="pb-1" onFocus={() => setFocused(key)}>
            {node}
          </div>
        )}
      </Virtualizer>
    </div>
  )
}
