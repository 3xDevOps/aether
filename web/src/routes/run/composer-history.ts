import { useEffect, useMemo, useState } from 'react'
import type * as React from 'react'
import type { RoomMessage } from '@/lib/types'
import { useStore } from '@/store'

const noMessages: RoomMessage[] = []

/** The member's own prompts on the run, newest first, without repeats in a row. */
export function sentPrompts(messages: RoomMessage[], selfID: string | null): string[] {
  const bodies = messages
    .filter((m) => m.kind === 'steer_request' && m.actor_id === selfID && m.body.trim())
    .map((m) => m.body)
    .reverse()
  return bodies.filter((body, index) => body !== bodies[index - 1])
}

export function usePromptHistory(runID: string, body: string, setBody: (next: string) => void) {
  const selfID = useStore((s) => s.info?.member.id ?? null)
  const messages = useStore((s) => s.roomMessages[runID] ?? noMessages)
  const prompts = useMemo(() => sentPrompts(messages, selfID), [messages, selfID])
  const [walk, setWalk] = useState<{ prompts: string[]; index: number; shown: string; draft: string }>()
  const walking = walk && walk.shown === body ? walk : undefined

  useEffect(() => {
    if (walk && walk.shown !== body) setWalk(undefined)
  }, [walk, body])

  return (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.nativeEvent.isComposing) return
    const field = event.currentTarget
    if (field.selectionStart !== field.selectionEnd) return
    const list = walking?.prompts ?? prompts
    let index: number
    if (event.key === 'ArrowUp') {
      if (!walking && body && field.selectionStart !== 0) return
      index = walking ? walking.index + 1 : 0
      if (index >= list.length) return
    } else {
      if (!walking) return
      index = walking.index - 1
    }
    event.preventDefault()
    const draft = walking?.draft ?? body
    const next = index < 0 ? draft : list[index]!
    setWalk(index < 0 ? undefined : { prompts: list, index, shown: next, draft })
    setBody(next)
    requestAnimationFrame(() => field.setSelectionRange(next.length, next.length))
  }
}
