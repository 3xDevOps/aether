import { act, renderHook } from '@testing-library/react'
import { useImplicitControl, type AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { run, serverInfo } from '@/test/fixtures'

function agent(localControl: boolean, takeControl = vi.fn()): AgentTerminal {
  return { localControl, steerable: true, controlUnavailable: false, session: { takeControl } } as unknown as AgentTerminal
}

describe('useImplicitControl', () => {
  beforeEach(() => useStore.setState({ info: serverInfo, roomStatus: {} }))

  it('runs what waited for control in order, each after the one before settles', async () => {
    const owned = { ...run({ controller_member_id: '' }), stateChangedAt: '2026-08-14T10:00:00Z', stateChangedAtEstimated: false }
    const takeControl = vi.fn()
    const { result, rerender } = renderHook(({ held }) => useImplicitControl(owned, agent(held, takeControl)), { initialProps: { held: false } })
    const done: string[] = []
    let settle!: () => void
    act(() => {
      result.current.withControl(() => new Promise<void>((resolve) => {
        settle = () => {
          done.push('option')
          resolve()
        }
      }))
      result.current.withControl(() => done.push('send'))
    })
    expect(takeControl).toHaveBeenCalled()
    expect(done).toEqual([])

    rerender({ held: true })
    await act(async () => {})
    expect(done).toEqual([])
    await act(async () => settle())
    expect(done).toEqual(['option', 'send'])
  })
})
