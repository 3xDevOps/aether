// Finding 1: the Chat transcript still renders done and deleted comments, and
// the RPC confirm re-adds a comment the deleted event already dropped.
// Copy into web/src/routes/run/ on the PR #798 branch and run with vitest.
import { rowsForRun } from '@/store/sessions'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { roomMessage, run } from '@/test/fixtures'

const record = toRecord(run())

it('renders only open comments as transcript note rows', () => {
  const room = [
    roomMessage({ id: 'open', kind: 'comment', body: 'open', created_at: '2026-08-14T10:01:00Z' }),
    roomMessage({ id: 'done', kind: 'comment', body: 'done body', resolution: 'done', created_at: '2026-08-14T10:02:00Z' }),
    roomMessage({ id: 'gone', kind: 'comment', body: 'deleted body', resolution: 'deleted', created_at: '2026-08-14T10:03:00Z' }),
  ]
  const rows = rowsForRun({ run: record, events: [], room, paused: false, memberName: (id: string) => id } as never)
  // Fails on the PR: ["open","done body","deleted body"].
  expect(rows.filter((r) => r.kind === 'note').map((r) => (r as { body: string }).body)).toEqual(['open'])
})

it('keeps a deleted comment out of the store when the event lands before the RPC answer', () => {
  const m = roomMessage({ id: 'gone', kind: 'comment', body: 'deleted body' })
  useStore.setState({ roomMessages: { [m.run_id]: [m] } })
  useStore.getState().dropRoomMessage(m.run_id, m.id)
  useStore.getState().upsertRoomMessage({ ...m, resolution: 'deleted' })
  // Fails on the PR: [["gone","deleted"]].
  expect(useStore.getState().roomMessages[m.run_id].map((x) => [x.id, x.resolution])).toEqual([])
})
