import { api, type Api } from '@/lib/api'
import type { SessionLease } from '@/lib/session-types'
import type { RoomMessage, Run } from '@/lib/types'
import { useStore } from '@/store'

// A run's send the server has not answered yet. Sending the same text again
// reuses its key, so a lost response cannot deliver the message twice.
const unanswered = new Map<string, { body: string; key: string }>()

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
  const pending = unanswered.get(run.id)
  const key = pending?.body === body ? pending.key : crypto.randomUUID()
  unanswered.set(run.id, { body, key })
  const { message } = await client.runRoomPost({
    workspace_id: run.workspace_id,
    run_id: run.id,
    kind: 'steer_request',
    body,
    idempotency_key: key,
    ...lease,
  })
  if (unanswered.get(run.id)?.key === key) unanswered.delete(run.id)
  useStore.getState().upsertRoomMessage(message)
  return message
}
