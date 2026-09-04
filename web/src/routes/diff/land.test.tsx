import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { Land } from '@/routes/diff/land'
import { api } from '@/lib/api'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi() }
})

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

test('offers switching to a pulled branch and reports success', async () => {
  const record = toRecord(run({ id: 'run_1', last_commit: 'a'.repeat(40) }))
  useStore.setState({
    runs: { [record.id]: record },
    pulls: {
      [record.id]: {
        branch: record.branch,
        ref: `refs/remotes/aether/${record.branch}`,
        output: '',
        current: false,
        dirty: false,
      },
    },
    capabilities: { gateway: 'desktop', methods: [], ws: [], local: ['pull.switch'] },
  })

  render(<Land run={record} />)
  expect(screen.getByText((_, element) => element?.textContent === `Branch ${record.branch} is on your machine`)).toBeTruthy()

  fireEvent.click(screen.getByRole('button', { name: 'Switch to it' }))
  await waitFor(() => expect(api.localPullSwitch).toHaveBeenCalledWith(record.id))
  expect(toast.success).toHaveBeenCalledWith(`Now on ${record.branch}`)
  expect(screen.getByText("You're on it")).toBeTruthy()
})
