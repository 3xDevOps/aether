import { useEffect, useRef, useState } from 'react'

const announceDelay = 1500
const announceAtMost = 3

/**
 * One polite line for items that changed into an announced state: gathered
 * for 1.5s from the first transition (a live stream never extends the
 * window), urgent states first, at most three.
 */
export function useAnnouncement<S>(
  items: ReadonlyMap<string, { state: S; label: string }>,
  announced: ReadonlySet<S>,
  urgent: ReadonlySet<S>,
  word: (state: S) => string,
): string {
  const [text, setText] = useState('')
  const before = useRef<ReadonlyMap<string, { state: S; label: string }> | null>(null)
  const queue = useRef<{ text: string; urgent: boolean }[]>([])
  const timer = useRef<number | undefined>(undefined)
  const latest = useRef({ announced, urgent, word })
  latest.current = { announced, urgent, word }
  useEffect(() => () => clearTimeout(timer.current), [])
  useEffect(() => {
    const previous = before.current
    before.current = items
    if (!previous) return
    const { announced, urgent, word } = latest.current
    for (const [id, { state, label }] of items) {
      const was = previous.get(id)
      if (was && was.state !== state && announced.has(state)) queue.current.push({ text: `${label}: ${word(state)}`, urgent: urgent.has(state) })
    }
    if (queue.current.length === 0 || timer.current !== undefined) return
    timer.current = window.setTimeout(() => {
      timer.current = undefined
      const ordered = [...queue.current.filter((item) => item.urgent), ...queue.current.filter((item) => !item.urgent)]
      setText(ordered.slice(0, announceAtMost).map((item) => item.text).join('. '))
      queue.current = []
    }, announceDelay)
  }, [items])
  return text
}
