import { connectSessionStream, type SessionStream } from '@/lib/acp-stream'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { SessionLease } from '@/lib/session-types'
import type { TakeoverAction } from '@/routes/terminal/attach'
import type { RootStore } from '@/store'
import { batchNotifications } from '@/store/batch'

interface Owned {
  stream: SessionStream
  users: number
  sessionID: string
  autoWrite: boolean
  askedWrite: boolean
}

/** Sockets live here, not in a view, so switching views keeps the stream. */
const owned = new Map<string, Owned>()
const controlSessions = new Map<string, string>()

const historyPage = 200

function controlSession(runID: string): string {
  let id = controlSessions.get(runID)
  if (!id) {
    id = `acp-${crypto.randomUUID()}`
    controlSessions.set(runID, id)
  }
  return id
}

export function sessionStreamOpen(runID: string): boolean {
  return owned.has(runID)
}

export function subscribeSession(store: RootStore, runID: string, autoWrite: boolean): () => void {
  let entry = owned.get(runID)
  if (entry) {
    entry.users++
  } else {
    const sessionID = controlSession(runID)
    const created: Owned = { users: 1, sessionID, autoWrite, askedWrite: false, stream: null as unknown as SessionStream }
    entry = created
    owned.set(runID, created)
    created.stream = connectSessionStream(runID, {
      afterSeq: () => store.getState().acpSessions[runID]?.seq ?? 0,
      lease: () => {
        const control = store.getState().acpSessions[runID]?.control
        return control?.has_control
          ? { write: true, control_session_id: sessionID, control_generation: control.control_generation }
          : { control_session_id: sessionID }
      },
      onAck: (ack) => {
        void batchNotifications(store, async () => {
          store.getState().acpAck(runID, ack)
          store.getState().acpControl(runID, {
            control_session_id: sessionID,
            control_generation: ack.control_generation ?? 0,
            has_control: ack.has_control,
          })
        })
        if (created.autoWrite && !created.askedWrite && !ack.has_control) {
          created.askedWrite = true
          created.stream.control(true)
        }
      },
      onFrames: (frames) => {
        void batchNotifications(store, async () => store.getState().acpFrames(runID, frames))
      },
      onControl: (frame) => {
        const held = store.getState().acpSessions[runID]?.control
        if (frame.request_id !== undefined && !frame.ok) {
          store.getState().acpControl(runID, held, frame.error)
          return
        }
        const has = frame.has_control === true
        store.getState().acpControl(runID, {
          control_session_id: sessionID,
          control_generation: frame.control_generation ?? held?.control_generation ?? 0,
          has_control: has,
          loss: !has && frame.revocation_reason === 'takeover' ? 'takeover' : undefined,
        })
      },
      onTakeover: (frame) => {
        const takeover = frame.takeover
        const active = takeover && (takeover.phase === 'holding' || takeover.phase === 'review')
        store.getState().acpTakeover(
          runID,
          active ? { ...takeover, receivedAt: performance.now() } : undefined,
          frame.ok === false ? frame.error ?? 'Takeover request refused' : takeover?.phase === 'denied' ? 'The controller denied your takeover request.' : undefined,
        )
        if (takeover?.phase === 'granted' && takeover.requester_session_id === sessionID) created.stream.control(true)
      },
      onState: (state, error) => store.getState().acpStream(runID, state, error),
    })
  }
  const held = entry
  return () => {
    if (--held.users > 0) return
    held.stream.close()
    owned.delete(runID)
  }
}

export function requestSessionControl(runID: string, write: boolean, takeover = false): boolean {
  return owned.get(runID)?.stream.control(write, { takeover }) ?? false
}

export function requestSessionTakeover(runID: string, action: TakeoverAction, id: string, generation?: number): boolean {
  return owned.get(runID)?.stream.takeover(action, id, generation) ?? false
}

export function sessionLease(store: RootStore, runID: string): SessionLease | undefined {
  const control = store.getState().acpSessions[runID]?.control
  return control?.has_control
    ? { control_session_id: control.control_session_id, control_generation: control.control_generation }
    : undefined
}

export async function loadOlderItems(store: RootStore, client: Api, runID: string): Promise<void> {
  const session = store.getState().acpSessions[runID]
  if (!session?.more || session.olderLoading) return
  store.getState().acpOlderState(runID, true)
  try {
    const frames = await client.runACPHistory(runID, session.oldestSeq, historyPage)
    store.getState().acpOlder(runID, frames, frames.length === historyPage && (frames[0]?.seq ?? 0) > 1)
  } catch (err) {
    store.getState().acpOlderState(runID, false, message(err))
  }
}

export async function loadWholeItem(store: RootStore, client: Api, runID: string, seq: number): Promise<void> {
  store.getState().acpReplaceItem(runID, await client.runACPItem(runID, seq))
}
