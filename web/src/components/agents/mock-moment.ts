import type { SessionItem } from '@/lib/session-types'

const item = (seq: number, over: Partial<SessionItem>): SessionItem => ({
  seq,
  epoch: 1,
  time: '2026-10-06T09:00:00Z',
  turn: 1,
  kind: 'message',
  ...over,
})

export const mockMoment: SessionItem[] = [
  item(1, { kind: 'message', message: { role: 'user', message_id: 'u1', text: 'Fix the flaky login test', complete: true } }),
  item(2, { kind: 'tool_call', tool_call: { id: 't1', title: 'Read auth/login_test.go', tool_kind: 'read', status: 'completed', output: 'Read 84 lines' } }),
  item(3, { kind: 'message', message: { role: 'assistant', message_id: 'a1', text: 'The test races the session cleanup. Running it to confirm.', complete: true } }),
  item(4, { kind: 'tool_call', tool_call: { id: 't2', title: 'Run go test ./auth/...', tool_kind: 'execute', status: 'completed', output: 'ok  example.com/auth  0.41s' } }),
]
