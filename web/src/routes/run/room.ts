import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { RoomMessageKind, Run } from '@/lib/types'
import type { ControlMetadata } from '@/routes/terminal/attach'
import { useStore } from '@/store'

const presencePollMs = 10_000
const presenceTimeoutMs = 15_000

export interface RoomPost {
  kind: Extract<RoomMessageKind, 'comment' | 'steer_request' | 'reply'>
  body: string
  correlationID?: string
  attachments?: string[]
}

export interface RunRoom {
  post: (post: RoomPost) => Promise<boolean>
  decide: (messageID: string, decision: 'approve' | 'deny') => Promise<void>
  busy: boolean
  error: string | undefined
  /** A failed steer is shown at the composer that sent it, every other failure in Details. */
  errorFromComposer: boolean
  clearError: () => void
}

export function useRunRoom(run: Run, control: ControlMetadata | undefined, client: Api = api): RunRoom {
  const runID = run.id
  const workspaceID = run.workspace_id
  const error = useStore((s) => s.roomActionError[runID])
  const [busy, setBusy] = useState(false)
  const [errorFromComposer, setErrorFromComposer] = useState(false)
  const retry = useRef<{ post: RoomPost; key: string } | null>(null)
  const controlRef = useRef(control)
  controlRef.current = control

  useEffect(() => {
    let active = true
    const s = useStore.getState()
    s.initializeRoomPagination(runID)
    s.setRoomLoading(runID, true)
    client.runRoomList({ workspace_id: workspaceID, run_id: runID, limit: 100 }).then(
      (page) => {
        if (active) useStore.getState().setRoomPage(runID, page.messages, page.next_before)
      },
      (cause) => {
        if (active) useStore.getState().setRoomError(runID, message(cause))
      },
    ).finally(() => {
      if (active) useStore.getState().setRoomLoading(runID, false)
    })
    return () => {
      active = false
    }
  }, [runID, workspaceID, client])

  useEffect(() => {
    let active = true
    let pending: AbortController | undefined
    const refresh = async () => {
      if (pending) return
      const abort = new AbortController()
      pending = abort
      const deadline = window.setTimeout(() => abort.abort(new Error('Presence request timed out after 15 seconds')), presenceTimeoutMs)
      try {
        const next = await client.runRoomStatus({ workspace_id: workspaceID, run_id: runID }, abort.signal)
        if (!active) return
        useStore.getState().setRoomStatus(runID, next, control)
        useStore.getState().setRoomStatusError(runID)
      } catch (cause) {
        if (active) useStore.getState().setRoomStatusError(runID, message(cause))
      } finally {
        window.clearTimeout(deadline)
        pending = undefined
      }
    }
    void refresh()
    const timer = window.setInterval(() => void refresh(), presencePollMs)
    return () => {
      active = false
      window.clearInterval(timer)
      pending?.abort()
    }
  }, [runID, workspaceID, control, client])

  const post = useCallback(async (next: RoomPost) => {
    const live = run.status === 'running' || run.status === 'needs-attention'
    const held = controlRef.current
    const previous = retry.current
    const key = previous && previous.post.kind === next.kind && previous.post.body === next.body &&
      previous.post.correlationID === next.correlationID &&
      (previous.post.attachments ?? []).join('\n') === (next.attachments ?? []).join('\n') ? previous.key : crypto.randomUUID()
    retry.current = { post: next, key }
    setBusy(true)
    setErrorFromComposer(next.kind === 'steer_request')
    useStore.getState().setRoomActionError(runID)
    try {
      const result = await client.runRoomPost({
        workspace_id: workspaceID,
        run_id: runID,
        kind: next.kind,
        body: next.body,
        correlation_id: next.correlationID,
        attachments: next.attachments?.length ? next.attachments : undefined,
        idempotency_key: key,
        ...(next.kind === 'steer_request' && live && held?.has_control && held.control_generation > 0
          ? { control_session_id: held.control_session_id, control_generation: held.control_generation }
          : {}),
      })
      useStore.getState().upsertRoomMessage(result.message)
      retry.current = null
      return true
    } catch (cause) {
      useStore.getState().setRoomActionError(runID, message(cause))
      return false
    } finally {
      setBusy(false)
    }
  }, [client, run.status, runID, workspaceID])

  const decide = useCallback(async (messageID: string, decision: 'approve' | 'deny') => {
    const held = controlRef.current
    setBusy(true)
    setErrorFromComposer(false)
    useStore.getState().setRoomActionError(runID)
    try {
      const result = await client.runRoomDecide({
        message_id: messageID,
        decision,
        control_session_id: held?.control_session_id ?? '',
        control_generation: held?.control_generation ?? 0,
      })
      useStore.getState().upsertRoomMessage(result.message)
    } catch (cause) {
      useStore.getState().setRoomActionError(runID, message(cause))
    } finally {
      setBusy(false)
    }
  }, [client, runID])

  const clearError = useCallback(() => useStore.getState().setRoomActionError(runID), [runID])
  return { post, decide, busy, error, errorFromComposer, clearError }
}
