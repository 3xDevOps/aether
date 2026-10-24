import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type * as React from 'react'
import { api } from '@/lib/api'
import type * as apiModule from '@/lib/api'
import type { GatewayCapabilities } from '@/lib/types'
import { Composer } from '@/routes/run/composer'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, run, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const every: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }
const closed = { id: 'run_1', status: 'merged', reason: 'closed; retained container', finished_at: '2026-08-14T10:30:00Z', container_retained_until: '2026-08-21T10:30:00Z' } as const

function mount(over: Partial<ReturnType<typeof run>>, methods = every) {
  const record = toRecord(run({ ...over }))
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    runs: { [record.id]: record },
    capabilities: methods,
    info: { ...serverInfo, member: alice },
    hydrated: true,
  })
  const Live = () => {
    const current = useStore((s) => s.runs[record.id]!)
    return <Composer run={current} agent={{} as never} room={{} as never} textarea={{ current: null } as React.RefObject<HTMLTextAreaElement | null>} onFocusChange={() => {}} onEscape={() => {}} />
  }
  return render(<Live />)
}

beforeEach(() => vi.clearAllMocks())

describe.each([{ mode: 'tui', acp: false }, { mode: 'acp', acp: true }] as const)('finished $mode run', (kind) => {
  it('offers Relaunch inside the retention grace and returns to a live run', async () => {
    vi.mocked(api.runRelaunch).mockResolvedValue(run({ ...kind, id: 'run_1', status: 'running', reason: undefined, finished_at: undefined, status_seq: 99 }))
    mount({ ...kind, ...closed })
    expect(screen.getByRole('status').textContent).toContain('Relaunch it to continue the chat and files.')

    fireEvent.click(screen.getByRole('button', { name: 'Relaunch' }))

    await waitFor(() => expect(useStore.getState().runs.run_1!.status).toBe('running'))
    expect(api.runRelaunch).toHaveBeenCalledWith('run_1')
    expect(screen.queryByRole('button', { name: 'Relaunch' })).toBeNull()
    expect(useStore.getState().runs.run_1!.container_retained_until).toBeUndefined()
  })

  it('shows the server refusal and keeps the button', async () => {
    vi.mocked(api.runRelaunch).mockRejectedValue(new Error('run.relaunch: retained container unavailable'))
    mount({ ...kind, ...closed })

    fireEvent.click(screen.getByRole('button', { name: 'Relaunch' }))

    expect((await screen.findByRole('alert')).textContent).toBe('run.relaunch: retained container unavailable')
    expect(screen.getByRole('button', { name: 'Relaunch' })).toHaveProperty('disabled', false)
  })

  it('offers nothing once the container is released', () => {
    mount({ ...kind, ...closed, reason: 'closed', container_retained_until: undefined })
    expect(screen.getByRole('status').textContent).toBe('This run has finished.')
    expect(screen.queryByRole('button', { name: 'Relaunch' })).toBeNull()
  })

  it('offers nothing when the gateway lacks run.relaunch', () => {
    mount({ ...kind, ...closed }, { gateway: 'remote', methods: ['run.get'], ws: [] })
    expect(screen.queryByRole('button', { name: 'Relaunch' })).toBeNull()
  })
})
