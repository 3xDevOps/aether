import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type { RunGitStatusResult, RunRepoCommandOutput } from '@/lib/types'
import { PublishDialog } from '@/routes/diff/publish-dialog'
import { useStore } from '@/store'
import { fakeApi, run, serverInfo, workspace } from '@/test/fixtures'
import { pickOption } from '@/test/select'

const output: RunRepoCommandOutput = { exit_code: 0, truncated: false }
const status: RunGitStatusResult = {
  branch: 'aether/reviewed', head: 'a'.repeat(40), detached: false, unborn: false,
  changes: [{ path: 'selected.txt', index: ' ', worktree: 'M', untracked: false, conflicted: false }],
  truncated: false, account_member_id: serverInfo.member.id, account_name: 'Run account', identity: 'reviewed-login',
  remotes: [{ name: 'fork', fetch_urls: ['https://github.com/fork/project.git'], push_urls: ['https://github.com/fork/project.git'] }], output,
}
const reviewedState = { state: { branch: status.branch, head: status.head }, output }

const origin = 'https://github.com/fork/project.git'
/** A checkout with nothing left to commit, in a workspace whose Origin is a fork. */
const committed: RunGitStatusResult = {
  ...status, changes: [], head_subject: 'fix the flaky scheduler test',
  remotes: [{ name: 'origin', fetch_urls: [origin], push_urls: [origin] }],
}
const pullRequest = {
  number: 7, url: 'https://github.com/upstream/project/pull/7', state: 'open', title: 'fix the flaky scheduler test', draft: false,
  repository: 'upstream/project', base_branch: 'main', head_repository: 'fork/project', head_branch: status.branch, head_oid: status.head,
}

beforeEach(() => {
  useStore.setState({ info: serverInfo, capabilities: { gateway: 'remote', methods: ['*'], ws: [] }, workspaces: { [workspace.id]: workspace } })
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

it('names the checkout and the account above both steps and commits only what was reviewed', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runGitDiff: vi.fn(async () => reviewedState),
    runGitCommit: vi.fn(async () => ({ committed: true, head: 'b'.repeat(40), index_updated: true, hooks_run: false, output })),
  })
  renderOpen(client)

  const checkout = await screen.findByRole('region', { name: 'Run checkout' })
  await within(checkout).findByText('aether/reviewed')
  expect(checkout.textContent).toBe('aether/reviewed at aaaaaaaa · as Run account · GitHub reviewed-login')
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

const none = { identity: status.identity, account_member_id: status.account_member_id, pull_request: null, output }
const pushedTo = (target: { remote: string; repository: string; head_branch: string }) => ({ pushed: true, head: status.head, target, output })

function destination() {
  return screen.getByRole('region', { name: 'Destination' })
}

it('opens on the push as two sentences, and one click pushes and opens the pull request', async () => {
  useStore.setState({ workspaces: { [workspace.id]: { ...workspace, origin } } })
  const target = { repository: 'upstream/project', base_branch: 'main', head_repository: 'fork/project', head_branch: status.branch }
  const push = { remote: 'origin', repository: origin, head_branch: status.branch }
  const client = fakeApi({
    runGitStatus: vi.fn(async () => committed),
    workspaceMirrorStatus: vi.fn(async () => ({ enabled: true, source_url: 'https://github.com/upstream/project.git' })),
    runGitPush: vi.fn(async () => pushedTo(push)),
    runPRStatus: vi.fn().mockResolvedValueOnce(none).mockResolvedValue({ ...none, pull_request: pullRequest }),
    runPRCreate: vi.fn(async () => ({ ...none, pull_request: pullRequest, created: true, reconciled: false, creation_uncertain: false })),
  })
  renderOpen(client)

  const action = await screen.findByRole('button', { name: 'Push and open pull request' })
  expect(screen.getByRole('tab', { name: '2 · Push and pull request' }).getAttribute('aria-selected')).toBe('true')
  await waitFor(() => expect(action).toHaveProperty('disabled', false))
  expect(client.runPRStatus).toHaveBeenCalledWith({ run_id: run().id, expected: { branch: status.branch, head: status.head }, target })
  expect(destination().textContent).toBe(
    `Pushes commit aaaaaaaa to ${origin} as branch ${status.branch}. Never a force push.` +
    `Opens a pull request into upstream/project:main from fork/project:${status.branch} as reviewed-login. It is not merged from here.` +
    'Change destination',
  )
  expect(screen.queryByLabelText('Push head branch')).toBeNull()
  expect(screen.getByLabelText('PR title')).toHaveProperty('value', 'fix the flaky scheduler test')

  fireEvent.click(action)
  expect(await screen.findByRole('link', { name: '#7: fix the flaky scheduler test' })).toHaveProperty('href', pullRequest.url)
  expect(client.runGitPush).toHaveBeenCalledWith({ run_id: run().id, expected: { branch: status.branch, head: status.head }, target: push })
  expect(client.runPRCreate).toHaveBeenCalledWith({
    run_id: run().id, expected: { branch: status.branch, head: status.head }, target,
    expected_login: 'reviewed-login', title: 'fix the flaky scheduler test', body: '', draft: false,
  })
  expect(screen.getByRole('region', { name: 'Push outcome' }).textContent).toContain('Pushed: yes')
  expect(screen.getByRole('region', { name: 'PR creation outcome' }).textContent).toContain('Created: yes')
  await waitFor(() => expect(screen.getByRole('button', { name: 'Push branch' })).toHaveProperty('disabled', false))
  expect(screen.queryByLabelText('PR title')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Push only' })).toBeNull()

  fireEvent.click(screen.getByRole('button', { name: 'Change destination' }))
  expect(screen.getByRole('combobox', { name: 'Push remote' }).textContent).toBe('origin')
  expect(screen.getByRole('combobox', { name: 'Writable push URL' }).textContent).toBe(origin)
  expect(screen.getByLabelText('Push head branch')).toHaveProperty('value', status.branch)
  const fields = within(screen.getByRole('group', { name: 'GitHub pull request' }))
  expect(['PR repository (owner/name)', 'PR base branch', 'PR head repository (owner/name)', 'PR head branch'].map((label) => (fields.getByLabelText(label) as HTMLInputElement).value))
    .toEqual(Object.values(target))
})

it('keeps a push that landed on screen when the pull request then fails, and offers only the pull request next', async () => {
  useStore.setState({ workspaces: { [workspace.id]: { ...workspace, origin } } })
  const refusal = 'runrepo: gh exited 1: gh: Validation Failed (HTTP 422)'
  const client = fakeApi({
    runGitStatus: vi.fn(async () => committed),
    runGitPush: vi.fn(async () => pushedTo({ remote: 'origin', repository: origin, head_branch: status.branch })),
    runPRStatus: vi.fn(async () => none),
    runPRCreate: vi.fn(async () => ({
      ...none, created: false, reconciled: false, creation_uncertain: false, error: refusal, output: { exit_code: 1, stderr: 'gh: Validation Failed (HTTP 422)', truncated: false },
    })),
  })
  renderOpen(client)
  const action = await screen.findByRole('button', { name: 'Push and open pull request' })
  await waitFor(() => expect(action).toHaveProperty('disabled', false))
  fireEvent.click(action)

  const outcome = await screen.findByRole('region', { name: 'PR creation outcome' })
  expect(outcome.textContent).toContain('Created: no')
  expect(within(outcome).getByText(refusal).getAttribute('role')).toBe('alert')
  expect(screen.getByRole('region', { name: 'Push outcome' }).textContent).toContain('Pushed: yes')
  await waitFor(() => expect(screen.getByRole('button', { name: 'Open pull request' })).toHaveProperty('disabled', false))
  expect(screen.queryByRole('button', { name: 'Push and open pull request' })).toBeNull()
  expect(client.runGitPush).toHaveBeenCalledTimes(1)
})

it('shows the pull request a run already has in place of the fields to open one', async () => {
  useStore.setState({ workspaces: { [workspace.id]: { ...workspace, origin } } })
  const client = fakeApi({
    runGitStatus: vi.fn(async () => committed),
    runPRStatus: vi.fn(async () => ({ ...none, pull_request: { ...pullRequest, repository: 'fork/project', url: 'https://github.com/fork/project/pull/7' } })),
  })
  renderOpen(client)

  expect(await screen.findByRole('link', { name: '#7: fix the flaky scheduler test' })).toBeTruthy()
  expect(destination().textContent).toContain(`Open · fork/project:${status.branch} → fork/project:main. Its checks and review comments are under #7 in the run’s header.`)
  expect(screen.queryByLabelText('PR title')).toBeNull()
  expect(screen.getByRole('button', { name: 'Push branch' })).toHaveProperty('disabled', false)
  expect(screen.queryByRole('button', { name: 'Open pull request' })).toBeNull()
})

it('opens the fields with the reason when no destination is known, and pushes only to the exact URL picked', async () => {
  const server = '/srv/aether/project.git'
  const client = fakeApi({
    runGitStatus: vi.fn(async () => ({ ...status, remotes: [...status.remotes!, { name: 'origin', fetch_urls: [server], push_urls: [server] }] })),
    runGitPush: vi.fn(async () => { throw new Error('run.git.push: remote rejected (protected branch hook declined)') }),
  })
  renderOpen(client)
  await screen.findByRole('checkbox', { name: 'Select selected.txt' })
  step('2 · Push and pull request')

  expect(destination().textContent).toContain('This workspace sets no checkout Origin, so no destination is chosen for you.')
  expect(screen.getByRole('button', { name: 'Push branch' })).toHaveProperty('disabled', true)
  await pickOption(screen.getByRole('combobox', { name: 'Push remote' }), 'fork')
  await waitFor(() => expect(screen.getByRole('combobox', { name: 'Writable push URL' }).textContent).toBe('https://github.com/fork/project.git'))
  fireEvent.change(screen.getByLabelText('Push head branch'), { target: { value: 'reviewed' } })
  expect(destination().textContent).toContain('Pushes commit aaaaaaaa to https://github.com/fork/project.git as branch reviewed. Never a force push.')
  const push = screen.getByRole('button', { name: 'Push only' })
  await waitFor(() => expect(push).toHaveProperty('disabled', false))
  fireEvent.click(push)

  const refused = await screen.findByText('run.git.push: remote rejected (protected branch hook declined)')
  expect(refused.getAttribute('role')).toBe('alert')
  expect(client.runGitPush).toHaveBeenCalledWith({
    run_id: run().id, expected: { branch: status.branch, head: status.head },
    target: { remote: 'fork', repository: 'https://github.com/fork/project.git', head_branch: 'reviewed' },
  })
  expect(screen.getByLabelText('Push head branch')).toHaveProperty('value', 'reviewed')
  expect(client.runPRStatus).not.toHaveBeenCalled()
})

it('keeps uncertain PR creation read-only with its exact target, across closing the dialog', async () => {
  const client = fakeApi({
    runGitStatus: vi.fn(async () => status),
    runPRStatus: vi.fn(async () => none),
    runPRCreate: vi.fn(async () => { throw new Error('connection lost after request') }),
  })
  renderOpen(client)
  await screen.findByRole('checkbox', { name: 'Select selected.txt' })
  step('2 · Push and pull request')
  for (const [label, value] of [['PR repository (owner/name)', 'upstream/project'], ['PR base branch', 'main'], ['PR head repository (owner/name)', 'fork/project'], ['PR head branch', 'reviewed']]) {
    fireEvent.change(screen.getByLabelText(label), { target: { value } })
  }
  expect(client.runPRStatus).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Discover existing PR' }))
  fireEvent.change(await screen.findByLabelText('PR title'), { target: { value: 'reviewed change' } })
  const create = screen.getByRole('button', { name: 'Open pull request' })
  await waitFor(() => expect(create).toHaveProperty('disabled', false))
  fireEvent.click(create)
  expect((await screen.findByText('connection lost after request')).getAttribute('role')).toBe('alert')

  fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  const opener = screen.getByRole('button', { name: 'Publish…' })
  expect(document.activeElement).toBe(opener)
  fireEvent.click(opener)
  step('2 · Push and pull request')

  const reconcile = await screen.findByRole('button', { name: 'Reconcile PR read-only' })
  expect(screen.getByLabelText('PR head repository (owner/name)').closest('fieldset')).toHaveProperty('disabled', true)
  expect(screen.queryByRole('button', { name: 'Open pull request' })).toBeNull()
  fireEvent.click(reconcile)
  await waitFor(() => expect(client.runPRStatus).toHaveBeenCalledTimes(2))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Reconcile PR read-only' })).toHaveProperty('disabled', false))
  expect(screen.getByLabelText('PR head branch')).toHaveProperty('value', 'reviewed')
  expect(client.runGitStatus).toHaveBeenCalledTimes(1)
  expect(client.runPRCreate).toHaveBeenCalledTimes(1)
})
