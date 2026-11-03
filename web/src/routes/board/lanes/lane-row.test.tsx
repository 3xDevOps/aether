import { render, screen, within } from '@testing-library/react'
import { Board } from '@/routes/board'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, otherWorkspace, run, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

describe('lane label', () => {
  it('marks a protected session with a lock and no other', () => {
    const runs = [run({ id: 'run_open', task: 'open session', status: 'running' }), run({ id: 'run_locked', task: 'locked session', status: 'running', protected: true })]
    useStore.setState({
      workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
      activeWorkspace: workspace.id,
      capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
      info: { ...serverInfo, member: alice },
      members: { [alice.id]: alice },
      runs: Object.fromEntries(runs.map((r) => [r.id, toRecord(r)])),
      boardViews: { [workspace.id]: 'lanes' },
      hydrated: true,
      hydrationError: null,
      route: { name: 'board', params: {} },
    })
    render(<Board />)

    const tree = screen.getByRole('tree', { name: 'Session lanes' })
    const lock = { name: 'Protected: only its owner or an admin can steer, message or close it' }
    expect(within(tree).getAllByRole('img', lock)).toHaveLength(1)
  })
})
