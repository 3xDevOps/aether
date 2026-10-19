import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { ControlMetadata } from '@/routes/terminal/attach'
import { useTakeover } from '@/routes/terminal/use-takeover'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

it('eases the hold fill logarithmically while the countdown stays linear', () => {
  const control: ControlMetadata = { control_session_id: 'me', control_generation: 1, has_control: false }
  const start = Date.parse('2026-01-01T00:00:00Z')
  const state = {
    id: 'r1',
    requester_member_id: 'm',
    requester_session_id: 'me',
    holder_session_id: 'other',
    holder_generation: 1,
    phase: 'holding' as const,
    hold_started_at: new Date(start).toISOString(),
    hold_deadline: new Date(start + 3000).toISOString(),
    server_now: new Date(start).toISOString(),
    receivedAt: performance.now(),
  }
  const { result } = renderHook(() =>
    useTakeover({ state, control, enabled: true, occupied: true, request: () => true }))

  act(() => { vi.advanceTimersByTime(1500) })
  expect(result.current.interaction.progress).toBeCloseTo(0.68, 2)
  expect(result.current.interaction.seconds).toBe(2)

  act(() => { vi.advanceTimersByTime(1516) })
  expect(result.current.interaction.progress).toBe(1)
})
