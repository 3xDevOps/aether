import { connectSessionStream } from '@/lib/acp-stream'
import type { Api } from '@/lib/api'
import type { AssistantFrame } from '@/lib/assistant-types'
import { message } from '@/lib/format'
import type { RootStore } from '@/store'
import { assistantSessionID } from '@/store/assistant'
import { batchNotifications } from '@/store/batch'

const historyPage = 1000

/** The one status read, made before a stream connects; the stream's status frames keep it current. */
export async function refreshAssistant(store: RootStore, client: Api): Promise<void> {
  const identity = store.getState().identityKey
  try {
    const status = await client.assistantStatus()
    if (store.getState().identityKey === identity) store.getState().setAssistantStatus(status)
  } catch (err) {
    if (store.getState().identityKey === identity) store.getState().setAssistantStatusError(message(err))
  }
}

/** Streams the assistant's items into `acpSessions` and its status frames into the assistant slice. */
export function connectAssistant(store: RootStore, client: Api): () => void {
  const id = assistantSessionID
  const stream = connectSessionStream(id, {
    afterSeq: () => store.getState().acpSessions[id]?.seq ?? 0,
    lease: () => ({}),
    onAck: (ack) => {
      void batchNotifications(store, async () => store.getState().acpAck(id, ack))
    },
    onFrames: (frames) => {
      const wire = frames as AssistantFrame[]
      void batchNotifications(store, async () => {
        for (const frame of wire) {
          if (frame.status) store.getState().setAssistantStatus(frame.status)
        }
        const items = wire.filter((frame) => frame.item || frame.reset)
        if (items.length > 0) store.getState().acpFrames(id, items)
      })
    },
    onControl: () => {},
    onTakeover: () => {},
    onState: (state, error) => store.getState().acpStream(id, state, error),
  }, {
    url: () => client.assistantSocket(),
    header: (afterSeq) => ({ after_seq: afterSeq }),
  })
  return () => {
    stream.close()
    void batchNotifications(store, async () => store.getState().acpStream(id, 'connecting'))
  }
}

export async function loadOlderAssistant(store: RootStore, client: Api): Promise<void> {
  const id = assistantSessionID
  const session = store.getState().acpSessions[id]
  if (!session?.more || session.olderLoading) return
  store.getState().acpOlderState(id, true)
  try {
    const page = await client.assistantHistory(session.oldestSeq, historyPage)
    if (store.getState().acpSessions[id]?.historyGeneration !== session.historyGeneration) return
    const oldest = page.frames.find((frame) => frame.item)?.item?.seq ?? 0
    store.getState().acpOlder(id, page.frames, oldest > (page.oldest_seq ?? 1), page.truncated_before)
  } catch (err) {
    if (store.getState().acpSessions[id]?.historyGeneration === session.historyGeneration) {
      store.getState().acpOlderState(id, false, message(err))
    }
  }
}
