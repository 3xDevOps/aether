import { sendToAgent } from '@/lib/send-to-agent'
import { useStore } from '@/store'
import { fakeApi, roomMessage, run } from '@/test/fixtures'

const target = run({ id: 'run_send' })

function keys(client: ReturnType<typeof fakeApi>): string[] {
  return vi.mocked(client.runRoomPost).mock.calls.map(([request]) => request.idempotency_key)
}

test('a retry keeps its key when another message went to the run in between', async () => {
  const client = fakeApi()
  vi.mocked(client.runRoomPost).mockRejectedValueOnce(new Error('connection lost after request'))

  await expect(sendToAgent(target, 'review', undefined, client)).rejects.toThrow('connection lost after request')
  await sendToAgent(target, 'feedback', undefined, client)
  await sendToAgent(target, 'review', undefined, client)
  await sendToAgent(target, 'review', undefined, client)

  const [lost, other, retried, again] = keys(client)
  expect(retried).toBe(lost)
  expect(other).not.toBe(lost)
  expect(again).not.toBe(lost)
})

test('an answer that outlives a sign-in stays out of the next member’s store, and so does the retry key', async () => {
  useStore.setState({ identityKey: 'alice', roomMessages: {} })
  const client = fakeApi()
  let answer!: (value: { message: ReturnType<typeof roomMessage> }) => void
  vi.mocked(client.runRoomPost)
    .mockRejectedValueOnce(new Error('connection lost after request'))
    .mockImplementationOnce(() => new Promise((resolve) => { answer = resolve }))

  await expect(sendToAgent(target, 'kept', undefined, client)).rejects.toThrow()
  const pending = sendToAgent(target, 'in flight', undefined, client)
  useStore.setState({ identityKey: 'bob' })
  answer({ message: roomMessage({ id: 'msg_alice', run_id: target.id, kind: 'steer_request' }) })
  await pending

  expect(useStore.getState().roomMessages[target.id]).toBeUndefined()
  await sendToAgent(target, 'kept', undefined, client)
  const [lost, , fresh] = keys(client)
  expect(fresh).not.toBe(lost)
})
