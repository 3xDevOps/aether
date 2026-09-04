import { act, render, screen, waitFor } from '@testing-library/react'
import { RunView } from '@/routes/run'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, run, workspace } from '@/test/fixtures'

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice },
    runs: { run_1: toRecord(run()) },
    envBuilds: {},
  })
}

describe('run view environment banner', () => {
  it('shows the building banner for the run workspace and clears on active', async () => {
    seed()
    render(<RunView params={{ runId: 'run_1' }} />)
    expect(screen.queryByText(/environment is still building/)).toBeNull()

    act(() =>
      useStore
        .getState()
        .startEnvBuild(workspace.id, { version: 2, status: 'building' }),
    )
    expect(
      await screen.findByText(/environment is still building/),
    ).toBeDefined()

    act(() =>
      useStore
        .getState()
        .applyEnvBuild(workspace.id, { version: 2, status: 'active' }),
    )
    await waitFor(() =>
      expect(screen.queryByText(/environment is still building/)).toBeNull(),
    )
  })

  it('falls back to the branch subtitle for titled TUI runs without a task', () => {
    seed()
    useStore.setState({
      runs: {
        run_1: toRecord(run({ task: '', title: 'Terminal title' })),
      },
    })
    render(<RunView params={{ runId: 'run_1' }} />)

    expect(screen.getByText('aether/run-1-checkout')).toBeDefined()
  })
})

it('shows the last commit row only when its timestamp is set', () => {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice },
    runs: {
      run_1: toRecord(
        run({ last_commit: 'a'.repeat(40), last_commit_at: null }),
      ),
    },
    envBuilds: {},
  })

  render(<RunView params={{ runId: 'run_1' }} />)
  expect(screen.queryByText('Last commit')).toBeNull()
})
