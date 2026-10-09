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
  autoRequest: boolean
  autoRetries: number
  retryTimer?: ReturnType<typeof setTimeout>
  stopWatch: () => void
  askOnce: () => void
}

const owned = new Map<string, Owned>()
const controlSessions = new Map<string, string>()

const historyPage = 200
const autoRetryLimit = 5

function controlSession(runID: string): string {
  let id = controlSessions.get(runID)
  if (!id) {
    id = `acp-${crypto.randomUUID()}`
    controlSessions.set(runID, id)
  }
  return id
}

function stopAutomaticControl(entry: Owned): void {
  entry.autoWrite = false
  entry.askedWrite = true
  clearTimeout(entry.retryTimer)
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
    const created: Owned = {
      users: 1, sessionID, autoWrite, askedWrite: false, autoRequest: false, autoRetries: 0,
      stream: null as unknown as SessionStream, stopWatch: () => {}, askOnce: () => {},
    }
    entry = created
    owned.set(runID, created)
    const askAutomatically = () => {
      clearTimeout(created.retryTimer)
      if (!created.autoWrite) return
      created.autoRequest = created.stream.control(true)
    }
    created.askOnce = () => {
      if (!created.autoWrite || created.askedWrite) return
      const control = store.getState().acpSessions[runID]?.control
      if (!control || control.has_control) return
      created.askedWrite = true
      askAutomatically()
    }
    const stopFree = store.subscribe((next, prior) => {
      const was = prior.runs[runID]?.controller_member_id
      const now = next.runs[runID]?.controller_member_id
      if (created.askedWrite && was && now === '' && !next.acpSessions[runID]?.control?.has_control) askAutomatically()
    })
    // A reload would otherwise leave the old page's lease held for the server's reconnect window.
    const release = () => {
      const control = store.getState().acpSessions[runID]?.control
      if (control?.has_control) created.stream.control(false, { generation: control.control_generation })
    }
    window.addEventListener('pagehide', release)
    created.stopWatch = () => {
      stopFree()
      window.removeEventListener('pagehide', release)
    }
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
        created.askOnce()
      },
      onFrames: (frames) => {
        void batchNotifications(store, async () => store.getState().acpFrames(runID, frames))
      },
      onControl: (frame) => {
        const session = store.getState().acpSessions[runID]
        const held = session?.control
        const automatic = frame.request_id !== undefined && created.autoRequest
        if (frame.request_id !== undefined) {
          created.autoRequest = false
          if (session?.takeoverError) store.getState().acpTakeover(runID, session.takeover)
        }
        if (frame.request_id !== undefined && !frame.ok) {
          if (!automatic) {
            store.getState().acpControl(runID, held, frame.error)
            return
          }
          const holder = store.getState().runs[runID]?.controller_member_id
          const self = store.getState().info?.member.id
          if (created.autoWrite && created.autoRetries < autoRetryLimit && (!holder || holder === self)) {
            created.retryTimer = setTimeout(askAutomatically, 1000 * 2 ** created.autoRetries++)
          }
          return
        }
        const has = frame.has_control === true
        if (!has && frame.revocation_reason) stopAutomaticControl(created)
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
        const requester = takeover?.requester_session_id === sessionID
        const session = store.getState().acpSessions[runID]
        if (requester && session?.controlError) store.getState().acpControl(runID, session.control)
        store.getState().acpTakeover(
          runID,
          active ? { ...takeover, receivedAt: performance.now() } : undefined,
          frame.ok === false ? frame.error ?? 'Takeover request refused' : requester && takeover?.phase === 'denied' ? 'The controller denied your takeover request.' : undefined,
        )
      },
      onState: (state, error) => store.getState().acpStream(runID, state, error),
    })
  }
  const held = entry
  return () => {
    if (--held.users > 0) return
    held.stream.close()
    held.stopWatch()
    clearTimeout(held.retryTimer)
    owned.delete(runID)
    void batchNotifications(store, async () => {
      store.getState().acpStream(runID, 'connecting')
      store.getState().acpControl(runID, undefined)
      store.getState().acpTakeover(runID, undefined)
    })
  }
}

export function allowSessionAutoWrite(runID: string): void {
  const entry = owned.get(runID)
  if (!entry || entry.autoWrite || entry.askedWrite) return
  entry.autoWrite = true
  entry.askOnce()
}

export function requestSessionControl(runID: string, write: boolean): boolean {
  const entry = owned.get(runID)
  if (!entry) return false
  stopAutomaticControl(entry)
  return entry.stream.control(write)
}

export function requestSessionTakeover(runID: string, action: TakeoverAction, id: string, generation?: number): boolean {
  const entry = owned.get(runID)
  if (!entry) return false
  if (action === 'start') stopAutomaticControl(entry)
  return entry.stream.takeover(action, id, generation)
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
    const page = await client.runACPHistory(runID, session.oldestSeq, historyPage)
    if (store.getState().acpSessions[runID]?.historyGeneration !== session.historyGeneration) return
    const oldest = page.frames.find((frame) => frame.item)?.item?.seq ?? 0
    store.getState().acpOlder(runID, page.frames, page.frames.length === historyPage && oldest > (page.oldest_seq ?? 1), page.truncated_before)
  } catch (err) {
    if (store.getState().acpSessions[runID]?.historyGeneration === session.historyGeneration) {
      store.getState().acpOlderState(runID, false, message(err))
    }
  }
}

export async function loadWholeItem(store: RootStore, client: Api, runID: string, seq: number): Promise<void> {
  store.getState().acpReplaceItem(runID, await client.runACPItem(runID, seq))
}
