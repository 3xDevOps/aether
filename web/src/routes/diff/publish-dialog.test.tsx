import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { RunGitStatusResult, RunRepoCommandOutput } from '@/lib/types'
import { PublishDialog } from '@/routes/diff/publish-dialog'
import { useStore } from '@/store'
import { fakeApi, run, serverInfo } from '@/test/fixtures'
import { pickOption } from '@/test/select'

const output: RunRepoCommandOutput = { exit_code: 0, truncated: false }
const status: RunGitStatusResult = {
  branch: 'aether/reviewed', head: 'a'.repeat(40), detached: false, unborn: false,
  changes: [{ path: 'selected.txt', index: ' ', worktree: 'M', untracked: false, conflicted: false }],
  truncated: false, account_member_id: serverInfo.member.id, account_name: 'Run account', identity: 'reviewed-login',
  remotes: [{ name: 'fork', fetch_urls: ['https://github.com/fork/project.git'], push_urls: ['https://github.com/fork/project.git'] }], output,
}
const reviewedState = { state: { branch: status.branch, head: status.head }, output }

beforeEach(() => {
  useStore.setState({ info: serverInfo, capabilities: { gateway: 'remote', methods: ['*'], ws: [] } })
})

function renderOpen(client: Api) {
  render(<PublishDialog run={run()} client={client} />)
  fireEvent.click(screen.getByRole('button', { name: 'Publish…' }))
}

function step(name: string) {
  fireEvent.mouseDown(screen.getByRole('tab', { name }), { button: 0 })
}

async function reviewSelected() {
  fireEvent.click(await screen.findByRole('checkbox', { name: 'Select selected.txt' }))
  fireEvent.click(screen.getByRole('button', { name: 'Review selected paths' }))
}

it('shows the checkout facts above both steps and commits only what was reviewed', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runGitDiff: vi.fn(async () => reviewedState),
    runGitCommit: vi.fn(async () => ({ committed: true, head: 'b'.repeat(40), index_updated: true, hooks_run: false, output })),
  })
  renderOpen(client)

  const checkout = within(await screen.findByRole('region', { name: 'Run checkout' }))
  expect(await checkout.findByText('aether/reviewed')).toBeTruthy()
  expect(checkout.getByText('a'.repeat(40))).toBeTruthy()
  expect(checkout.getByText('reviewed-login')).toBeTruthy()
  expect(screen.getByRole('tab', { name: '1 · Commit' }).getAttribute('aria-selected')).toBe('true')

  await reviewSelected()
  fireEvent.change(screen.getByLabelText('Commit message'), { target: { value: 'selected change' } })
  const commit = screen.getByRole('button', { name: 'Commit selected' })
  await waitFor(() => expect(commit).toHaveProperty('disabled', false))
  fireEvent.click(commit)

  expect(await screen.findByRole('region', { name: 'Commit outcome' })).toHaveProperty('textContent', expect.stringContaining('Committed: yes'))
  expect(client.runGitCommit).toHaveBeenCalledWith({
    run_id: run().id, expected: { branch: status.branch, head: status.head }, paths: ['selected.txt'], message: 'selected change',
  })
  fireEvent.click(screen.getByRole('button', { name: 'Continue to push' }))
  expect(screen.getByRole('tab', { name: '2 · Push and pull request' }).getAttribute('aria-selected')).toBe('true')
})

it('shows an RPC error under the button that sent it', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runGitDiff: vi.fn(async () => reviewedState),
    runGitCommit: vi.fn(async () => { throw new Error('run.git.commit: checkout is locked') }),
  })
  renderOpen(client)
  await reviewSelected()
  fireEvent.change(screen.getByLabelText('Commit message'), { target: { value: 'selected change' } })
  const commit = screen.getByRole('button', { name: 'Commit selected' })
  await waitFor(() => expect(commit).toHaveProperty('disabled', false))
  fireEvent.click(commit)

  const alert = await screen.findByText('run.git.commit: checkout is locked')
  expect(alert.getAttribute('role')).toBe('alert')
  expect(commit.parentElement!.contains(alert)).toBe(true)
  expect(screen.getByRole('button', { name: 'Review selected paths' }).parentElement!.contains(alert)).toBe(false)
})

it('withholds commit authority when the selected diff observes a different branch at the same HEAD', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runGitDiff: vi.fn(async () => ({ state: { branch: 'another-branch', head: status.head }, output })),
  })
  renderOpen(client)
  await reviewSelected()
  fireEvent.change(screen.getByLabelText('Commit message'), { target: { value: 'selected change' } })
  const alert = await screen.findByRole('alert')
  expect(alert.textContent).toContain('Branch or HEAD changed during review')
  expect(screen.getByRole('button', { name: 'Review selected paths' }).parentElement!.contains(alert)).toBe(true)
  expect(screen.getByRole('button', { name: 'Commit selected' })).toHaveProperty('disabled', true)
})

it('invalidates an earlier reviewed commit boundary even when status refresh fails', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn().mockResolvedValueOnce(status).mockRejectedValueOnce(new Error('checkout unavailable')),
    runGitDiff: vi.fn(async () => reviewedState),
  })
  renderOpen(client)
  await reviewSelected()
  fireEvent.change(screen.getByLabelText('Commit message'), { target: { value: 'selected change' } })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Commit selected' })).toHaveProperty('disabled', false))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh status' }))
  await screen.findByText('checkout unavailable')
  expect(screen.getByRole('button', { name: 'Commit selected' })).toHaveProperty('disabled', true)
})

it('pushes only to an exact remote URL after the attestation', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runGitPush: vi.fn(async () => ({ pushed: true, head: status.head, target: { remote: 'fork', repository: 'https://github.com/fork/project.git', head_branch: 'reviewed' }, output })),
  })
  renderOpen(client)
  await screen.findByRole('checkbox', { name: 'Select selected.txt' })
  step('2 · Push and pull request')

  await pickOption(screen.getByRole('combobox', { name: 'Push remote' }), 'fork')
  await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
  await pickOption(screen.getByRole('combobox', { name: 'Writable push URL' }), 'https://github.com/fork/project.git')
  fireEvent.change(screen.getByLabelText('Push head branch'), { target: { value: 'reviewed' } })
  const push = screen.getByRole('button', { name: 'Push reviewed branch' })
  expect(push).toHaveProperty('disabled', true)
  fireEvent.click(screen.getByRole('checkbox', { name: 'I reviewed the run account, branch, HEAD and exact push destination above.' }))
  await waitFor(() => expect(push).toHaveProperty('disabled', false))
  fireEvent.click(push)

  expect(await screen.findByRole('region', { name: 'Push outcome' })).toHaveProperty('textContent', expect.stringContaining('Pushed: yes'))
  expect(client.runGitPush).toHaveBeenCalledWith({
    run_id: run().id, expected: { branch: status.branch, head: status.head },
    target: { remote: 'fork', repository: 'https://github.com/fork/project.git', head_branch: 'reviewed' },
  })
})

it('keeps uncertain PR creation read-only with its exact target, across closing the dialog', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runPRStatus: vi.fn(async () => ({ identity: status.identity, account_member_id: status.account_member_id, pull_request: null, output })),
    runPRCreate: vi.fn(async () => { throw new Error('connection lost after request') }),
  })
  renderOpen(client)
  await screen.findByRole('checkbox', { name: 'Select selected.txt' })
  step('2 · Push and pull request')
  const pr = within(screen.getByRole('region', { name: 'GitHub pull request' }))
  for (const [label, value] of [['PR repository (owner/name)', 'upstream/project'], ['PR base branch', 'main'], ['PR head repository (owner/name)', 'fork/project'], ['PR head branch', 'reviewed']]) {
    fireEvent.change(pr.getByLabelText(label), { target: { value } })
  }
  fireEvent.click(pr.getByRole('button', { name: 'Discover existing PR' }))
  const consent = pr.getByRole('checkbox', { name: 'I reviewed this GitHub identity and the exact PR repository, base, fork and head.' })
  await waitFor(() => expect(consent).toHaveProperty('disabled', false))
  fireEvent.click(consent)
  fireEvent.change(pr.getByLabelText('PR title'), { target: { value: 'reviewed change' } })
  const create = pr.getByRole('button', { name: 'Create reviewed PR' })
  await waitFor(() => expect(create).toHaveProperty('disabled', false))
  fireEvent.click(create)
  const failure = await screen.findByText('connection lost after request')
  expect(create.parentElement!.contains(failure)).toBe(true)

  fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  const opener = screen.getByRole('button', { name: 'Publish…' })
  expect(document.activeElement).toBe(opener)
  fireEvent.click(opener)
  step('2 · Push and pull request')

  const again = within(await screen.findByRole('region', { name: 'GitHub pull request' }))
  expect(again.getByLabelText('PR head repository (owner/name)').closest('fieldset')).toHaveProperty('disabled', true)
  expect(again.getByRole('button', { name: 'Create reviewed PR' })).toHaveProperty('disabled', true)
  fireEvent.click(again.getByRole('button', { name: 'Reconcile PR read-only' }))
  await waitFor(() => expect(again.getByRole('button', { name: 'Reconcile PR read-only' })).toHaveProperty('disabled', false))
  expect(again.getByRole('button', { name: 'Create reviewed PR' })).toHaveProperty('disabled', true)
  expect(again.getByLabelText('PR head branch')).toHaveProperty('value', 'reviewed')
  expect(client.runGitStatus).toHaveBeenCalledTimes(1)
})

it('sends only checked PR feedback to the agent', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runPRFeedback: vi.fn(async () => ({
      identity: status.identity, account_member_id: status.account_member_id, output, truncated: false,
      pull_request: null, checks: [], reviews: [], review_comments: [],
      comments: [
        { id: 'c1', author: 'reviewer', body: 'please add a test', created_at: '2026-10-01', url: 'https://github.com/upstream/project/pull/7#c1' },
        { id: 'c2', author: 'reviewer', body: 'nit: rename', created_at: '2026-10-01', url: 'https://github.com/upstream/project/pull/7#c2' },
      ],
    })),
  })
  renderOpen(client)
  await screen.findByRole('checkbox', { name: 'Select selected.txt' })
  step('2 · Push and pull request')
  const pr = within(screen.getByRole('region', { name: 'GitHub pull request' }))
  for (const [label, value] of [['PR repository (owner/name)', 'upstream/project'], ['PR base branch', 'main'], ['PR head repository (owner/name)', 'fork/project'], ['PR head branch', 'reviewed']]) {
    fireEvent.change(pr.getByLabelText(label), { target: { value } })
  }
  fireEvent.click(pr.getByRole('button', { name: 'Refresh PR feedback' }))
  const feedback = within(await screen.findByRole('region', { name: 'PR feedback' }))
  fireEvent.click(within(feedback.getByText('please add a test').closest('article')!).getByRole('checkbox'))
  fireEvent.click(feedback.getByRole('button', { name: 'Send selected feedback to the agent' }))

  await feedback.findByRole('status')
  const body = vi.mocked(client.runRoomPost).mock.calls[0][0].body
  expect(body).toContain('please add a test')
  expect(body).not.toContain('nit: rename')
})
