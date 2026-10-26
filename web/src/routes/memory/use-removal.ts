import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { MemoryItem } from '@/lib/types'

/** How long Undo is offered before `memory.rm`, which cannot be undone, is sent. */
export const undoMs = 5000

type Pending = { item: MemoryItem; workspaceID: string; timer: ReturnType<typeof setTimeout> }

/**
 * Remove with undo: the note leaves the list at once and `memory.rm` waits
 * out the undo window. A second removal, a workspace switch, leaving the page
 * and closing the tab each send the waiting one at once. A note sent for
 * removal stays hidden from later reads; a refusal brings it back.
 */
export function useRemoval(client: Api, workspaceID: string, onSettled: () => void) {
  const [pending, setPending] = useState<MemoryItem | null>(null)
  const [gone, setGone] = useState<ReadonlySet<string>>(() => new Set())
  const entry = useRef<Pending | null>(null)
  const settled = useRef(onSettled)
  settled.current = onSettled

  const commit = useCallback(() => {
    const current = entry.current
    if (!current) return
    entry.current = null
    clearTimeout(current.timer)
    const { item } = current
    setPending(null)
    setGone((ids) => new Set(ids).add(item.id))
    client.memoryRm(current.workspaceID, item.id).then(
      () => settled.current(),
      (error: unknown) => {
        toast.error(message(error))
        setGone((ids) => {
          const next = new Set(ids)
          next.delete(item.id)
          return next
        })
        settled.current()
      },
    )
  }, [client])

  const remove = useCallback((item: MemoryItem) => {
    commit()
    entry.current = { item, workspaceID, timer: setTimeout(commit, undoMs) }
    setPending(item)
  }, [commit, workspaceID])

  const undo = useCallback(() => {
    if (!entry.current) return
    clearTimeout(entry.current.timer)
    entry.current = null
    setPending(null)
  }, [])

  /** memory.add answers a waiting note's own text with that note, which then has to stay. */
  const keep = useCallback((id: string) => {
    if (entry.current?.item.id === id) undo()
  }, [undo])

  useEffect(() => commit, [commit, workspaceID])

  useEffect(() => {
    window.addEventListener('pagehide', commit)
    return () => window.removeEventListener('pagehide', commit)
  }, [commit])

  const hidden = useMemo(() => (pending ? new Set(gone).add(pending.id) : gone), [gone, pending])
  return { pending, hidden, remove, undo, keep }
}
