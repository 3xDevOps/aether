import { api, type Api } from '@/lib/api'
import type { SessionLease } from '@/lib/session-types'
import type { RoomMessage, Run } from '@/lib/types'
import { useStore } from '@/store'

// The key of each send the server has not answered, by run and text. Sending
// the same text again reuses it, so a lost response cannot deliver the
// message twice, whatever else was sent to the run in between.
const unanswered = new Map<string, string>()

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) unanswered.clear()
})

/**
 * Sends `body` to the run's agent as one message, under the rules of the
 * Session composer. With the control `lease` this tab holds it is delivered
 * at once; without one it waits 45 seconds for the run's controller.
 *
 * The returned message is the receipt: `state` is `sent`, `queued`,
 * `not_sent` or `uncertain`, and `failure` says why. The store holds it too
 * and keeps it current.
 */
export async function sendToAgent(
  run: Pick<Run, 'id' | 'workspace_id'>,
  body: string,
  lease?: SessionLease,
  client: Api = api,
): Promise<RoomMessage> {
  const slot = `${run.id}\n${body}`
  const key = unanswered.get(slot) ?? crypto.randomUUID()
  unanswered.set(slot, key)
  const identity = useStore.getState().identityKey
  const { message } = await client.runRoomPost({
    workspace_id: run.workspace_id,
    run_id: run.id,
    kind: 'steer_request',
    body,
    idempotency_key: key,
    ...lease,
  })
  if (unanswered.get(slot) === key) unanswered.delete(slot)
  // An answer that outlived a sign-in belongs to the member who asked.
  if (useStore.getState().identityKey === identity) useStore.getState().upsertRoomMessage(message)
  return message
}
