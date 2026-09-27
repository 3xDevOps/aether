import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { RunGitStatusResult, RunRepoCommandOutput } from '@/lib/types'
import { NativeChanges } from './native-changes'
import { useStore } from '@/store'
import { fakeApi, run, serverInfo } from '@/test/fixtures'

const output: RunRepoCommandOutput = { exit_code: 0, truncated: false }
const status: RunGitStatusResult = {
  branch: 'aether/reviewed', head: 'a'.repeat(40), detached: false, unborn: false,
  changes: [{ path: 'selected.txt', index: ' ', worktree: 'M', untracked: false, conflicted: false }],
  truncated: false, account_member_id: serverInfo.member.id, account_name: 'Run account', identity: 'reviewed-login',
  remotes: [{ name: 'fork', fetch_urls: ['https://github.com/fork/project.git'], push_urls: ['https://github.com/fork/project.git'] }], output,
}

beforeEach(() => {
  useStore.setState({ info: serverInfo, capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
})

it('withholds commit authority when selected diff observes a different branch at the same HEAD', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runGitDiff: vi.fn(async () => ({ state: { branch: 'another-branch', head: status.head }, output })),
  })
  render(<NativeChanges run={run()} wrap client={client} />)
  fireEvent.click(screen.getByText('Native changes & publish'))
  fireEvent.click(await screen.findByRole('checkbox', { name: 'Select selected.txt' }))
  fireEvent.click(screen.getByRole('button', { name: 'Review selected paths' }))
  fireEvent.change(screen.getByLabelText('Commit message'), { target: { value: 'selected change' } })
  await screen.findByRole('alert')
  expect(screen.getByRole('button', { name: 'Commit selected paths' })).toHaveProperty('disabled', true)
})

it('invalidates an earlier reviewed commit boundary even when status refresh fails', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn().mockResolvedValueOnce(status).mockRejectedValueOnce(new Error('checkout unavailable')),
    runGitDiff: vi.fn(async () => ({ state: { branch: status.branch, head: status.head }, output })),
  })
  render(<NativeChanges run={run()} wrap client={client} />)
  fireEvent.click(screen.getByText('Native changes & publish'))
  fireEvent.click(await screen.findByRole('checkbox', { name: 'Select selected.txt' }))
  fireEvent.click(screen.getByRole('button', { name: 'Review selected paths' }))
  fireEvent.change(screen.getByLabelText('Commit message'), { target: { value: 'selected change' } })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Commit selected paths' })).toHaveProperty('disabled', false))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh native status' }))
  await screen.findByText('checkout unavailable')
  expect(screen.getByRole('button', { name: 'Commit selected paths' })).toHaveProperty('disabled', true)
})

it('keeps uncertain PR creation read-only with its exact target instead of permitting replay after an empty lookup', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runPRStatus: vi.fn(async () => ({ identity: status.identity, account_member_id: status.account_member_id, pull_request: null, output })),
    runPRCreate: vi.fn(async () => { throw new Error('connection lost after request') }),
  })
  render(<NativeChanges run={run()} wrap client={client} />)
  fireEvent.click(screen.getByText('Native changes & publish'))
  await screen.findByRole('checkbox', { name: 'Select selected.txt' })
  const pr = within(screen.getByRole('region', { name: 'GitHub pull request' }))
  for (const [label, value] of [['PR repository (owner/name)', 'upstream/project'], ['PR base branch', 'main'], ['PR head repository (owner/name)', 'fork/project'], ['PR head branch', 'reviewed']]) {
    fireEvent.change(pr.getByLabelText(label), { target: { value } })
  }
  fireEvent.click(pr.getByRole('button', { name: 'Discover existing PR' }))
  const consent = pr.getByRole('checkbox', { name: 'I reviewed this GitHub identity and the exact PR repository, base, fork and head.' })
  await waitFor(() => expect(consent).toHaveProperty('disabled', false))
  fireEvent.click(consent)
  fireEvent.change(pr.getByLabelText('PR title'), { target: { value: 'reviewed change' } })
  fireEvent.click(pr.getByRole('button', { name: 'Create reviewed PR' }))
  await screen.findByText('connection lost after request')
  expect(pr.getByLabelText('PR head repository (owner/name)').closest('fieldset')).toHaveProperty('disabled', true)
  expect(pr.getByRole('button', { name: 'Create reviewed PR' })).toHaveProperty('disabled', true)
  fireEvent.click(pr.getByRole('button', { name: 'Reconcile PR read-only' }))
  await waitFor(() => expect(pr.getByRole('button', { name: 'Reconcile PR read-only' })).toHaveProperty('disabled', false))
  expect(pr.getByRole('button', { name: 'Create reviewed PR' })).toHaveProperty('disabled', true)
  expect(pr.getByLabelText('PR head branch')).toHaveProperty('value', 'reviewed')
})
