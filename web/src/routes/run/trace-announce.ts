import { useEffect, useRef, useState } from 'react'
import type { Span, SpanStatus } from '@/store/trace'

const announceDelay = 1500
const announceAtMost = 3

function statuses(root: Span, into = new Map<string, { status: SpanStatus; span: Span }>()) {
  for (const child of root.children) {
    into.set(child.id, { status: child.status, span: child })
    statuses(child, into)
  }
  return into
}

/** A span first seen failed is news unless it starts before floor, the earliest session span held before: that is older history loading. */
function change(span: Span, was: SpanStatus | undefined, floor: number): { text: string; urgent: boolean } | undefined {
  if (span.status === was) return undefined
  if (span.status === 'failed' && (was !== undefined || span.start >= floor)) return { text: `${span.label}: failed`, urgent: true }
  if (span.kind !== 'turn') return undefined
  if (span.status === 'running' && was === undefined) return { text: `Turn ${span.turn} started`, urgent: false }
  if (was === 'running') return { text: `Turn ${span.turn} ${span.status === 'done' ? 'finished' : 'stopped'}`, urgent: false }
  return undefined
}

/** Turn starts and ends and new failures, batched over a window that opens at the first change and is never extended. */
export function useTraceAnnouncement(root: Span): string {
  const [text, setText] = useState('')
  const before = useRef<Map<string, { status: SpanStatus; span: Span }> | null>(null)
  const queue = useRef<{ text: string; urgent: boolean }[]>([])
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  useEffect(() => {
    const now = statuses(root)
    const previous = before.current
    before.current = now
    if (!previous) return
    let floor = Infinity
    for (const { span } of previous.values()) if (span.kind !== 'workers' && span.kind !== 'worker') floor = Math.min(floor, span.start)
    for (const [id, { span }] of now) {
      const said = change(span, previous.get(id)?.status, floor)
      if (said) queue.current.push(said)
    }
    if (queue.current.length === 0 || timer.current !== undefined) return
    timer.current = window.setTimeout(() => {
      timer.current = undefined
      const ordered = [...queue.current.filter((item) => item.urgent), ...queue.current.filter((item) => !item.urgent)]
      setText(ordered.slice(0, announceAtMost).map((item) => item.text).join('. '))
      queue.current = []
    }, announceDelay)
  }, [root])
  return text
}
