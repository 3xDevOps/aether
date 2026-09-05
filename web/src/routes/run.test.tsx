import { render, screen } from '@testing-library/react'
import { RunView } from '@/routes/run'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, run, workspace } from '@/test/fixtures'

function seed() {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice },
    runs: { run_1: toRecord(run()) },
  })
}


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
it('marks a protected run in the view header', () => {
  seed()
  useStore.setState({ runs: { run_1: toRecord(run({ protected: true })) } })

  render(<RunView params={{ runId: 'run_1' }} />)

  expect(
    screen.getByTitle('Protected: only the owner or an admin can steer or kill this run'),
  ).toBeDefined()
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
  })

  render(<RunView params={{ runId: 'run_1' }} />)
  expect(screen.queryByText('Last commit')).toBeNull()
})
