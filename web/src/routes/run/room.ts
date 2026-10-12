import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { RoomMessage, RoomMessageKind, Run } from '@/lib/types'
import type { ControlMetadata } from '@/routes/terminal/attach'
import { sameIdentity, useStore } from '@/store'
import { isProvisional } from '@/store/collaboration'
import { optimistic } from '@/store/optimistic'

const presencePollMs = 10_000
const presenceTimeoutMs = 15_000

export interface RoomPost {
  kind: Extract<RoomMessageKind, 'steer_request' | 'reply'>
  body: string
  correlationID?: string
  attachments?: string[]
  /** A caller that outlives this hook keeps its own retry key. */
  key?: string
}

export interface RunRoom {
  post: (post: RoomPost) => Promise<boolean>
  /** Resending a text whose note is not yet saved reuses its idempotency key. */
  note: (body: string) => Promise<void>
  decide: (messageID: string, decision: 'approve' | 'deny') => Promise<void>
  loadOlder: () => Promise<void>
  refresh: () => Promise<void>
  error: string | undefined
  /** A failed steer is shown at the composer that sent it, every other failure in Details. */
  errorFromComposer: boolean
  clearError: () => void
}

const pageLimit = 100

function overlaps(messages: RoomMessage[], cached: RoomMessage[]): boolean {
  const cachedIDs = new Set(cached.map((m) => m.id))
  return messages.some((m) => cachedIDs.has(m.id))
}

export function useRunRoom(run: Run, control: ControlMetadata | undefined, client: Api = api): RunRoom {
  const runID = run.id
  const workspaceID = run.workspace_id
  const error = useStore((s) => s.roomActionError[runID])
  const selfID = useStore((s) => s.info?.member.id ?? '')
  const [errorFromComposer, setErrorFromComposer] = useState(false)
  const live = useStore((s) => s.connection === 'live')
  const loaded = useRef(false)
  const retry = useRef<Partial<Record<string, { post: RoomPost; key: string }>>>({})
  const controlRef = useRef(control)
  controlRef.current = control

  useEffect(() => {
    let active = true
    const s = useStore.getState()
    s.initializeRoomPagination(runID)
    s.setRoomLoading(runID, true)
    client.runRoomList({ workspace_id: workspaceID, run_id: runID, limit: pageLimit }).then(
      (page) => {
        if (active) useStore.getState().setRoomPage(runID, page.messages, page.next_before)
      },
      (cause) => {
        if (active) useStore.getState().setRoomError(runID, message(cause))
      },
    ).finally(() => {
      loaded.current = true
      if (active) useStore.getState().setRoomLoading(runID, false)
    })
    return () => {
      active = false
    }
  }, [runID, workspaceID, client])

  const refresh = useCallback(async () => {
    const visited = new Set<string>()
    const cached = (useStore.getState().roomMessages[runID] ?? []).filter((m) => !isProvisional(m))
    useStore.getState().setRoomError(runID)
    let before: string | undefined
    try {
      for (;;) {
        const page = await client.runRoomList({ workspace_id: workspaceID, run_id: runID, before, limit: pageLimit })
        useStore.getState().setRoomPage(runID, page.messages, page.next_before, before !== undefined)
        // Each cursor is walked once so a malformed response cannot loop.
        if (!cached.length || overlaps(page.messages, cached) || !page.next_before || visited.has(page.next_before)) return
        visited.add(page.next_before)
        before = page.next_before
      }
    } catch (cause) {
      useStore.getState().setRoomError(runID, message(cause))
    }
  }, [client, runID, workspaceID])

  useEffect(() => {
    if (live && loaded.current) void refresh()
  }, [live, refresh])

  const loadOlder = useCallback(async () => {
    const state = useStore.getState()
    const before = state.roomNextBefore[runID]
    const pagination = state.roomPagination[runID]
    if (!pagination?.initialized || pagination.exhausted || before === undefined || state.roomLoading[runID]) return
    state.setRoomLoading(runID, true)
    state.setRoomError(runID)
    try {
      const page = await client.runRoomList({ workspace_id: workspaceID, run_id: runID, before, limit: pageLimit })
      useStore.getState().setRoomPage(runID, page.messages, page.next_before, true)
    } catch (cause) {
      useStore.getState().setRoomError(runID, message(cause))
    } finally {
      useStore.getState().setRoomLoading(runID, false)
    }
  }, [client, runID, workspaceID])

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
    const slot = `${next.kind}:${next.correlationID ?? ''}`
    const previous = retry.current[slot]
    const key = next.key ?? (previous && previous.post.body === next.body &&
      (previous.post.attachments ?? []).join('\n') === (next.attachments ?? []).join('\n') ? previous.key : crypto.randomUUID())
    retry.current[slot] = { post: next, key }
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
      delete retry.current[slot]
      return true
    } catch (cause) {
      useStore.getState().setRoomActionError(runID, message(cause))
      return false
    }
  }, [client, run.status, runID, workspaceID])

  const decide = useCallback(async (messageID: string, decision: 'approve' | 'deny') => {
    const held = controlRef.current
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
    }
  }, [client, runID])

  const note = useCallback(async (body: string) => {
    const same = sameIdentity()
    const keys = useStore.getState().noteKeys[runID]
    const key = keys && Object.hasOwn(keys, body) ? keys[body]! : crypto.randomUUID()
    useStore.getState().noteSending(runID, body, key)
    const now = new Date().toISOString()
    const provisional: RoomMessage = {
      id: key,
      idempotency_key: key,
      workspace_id: workspaceID,
      run_id: runID,
      actor_id: selfID,
      kind: 'comment',
      body,
      state: 'sent',
      created_at: now,
      updated_at: now,
    }
    const outcome = await optimistic({
      apply: () => useStore.getState().upsertRoomMessage(provisional),
      send: () => client.runRoomPost({ workspace_id: workspaceID, run_id: runID, kind: 'comment', body, idempotency_key: key }),
      confirm: (result) => same() && useStore.getState().upsertRoomMessage(result.message),
      revert: () => same() && useStore.getState().dropRoomMessage(runID, key),
    })
    if (outcome.ok || !same()) return
    useStore.getState().noteFailed(runID, { text: body, key, error: outcome.error, unanswered: outcome.unanswered })
    void outcome.refused?.then((error) => same() && useStore.getState().noteRefused(runID, key, error))
  }, [client, runID, selfID, workspaceID])

  const clearError = useCallback(() => useStore.getState().setRoomActionError(runID), [runID])
  return { post, note, decide, loadOlder, refresh, error, errorFromComposer, clearError }
}
