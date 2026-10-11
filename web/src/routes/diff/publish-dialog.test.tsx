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

it('opens on the push with every destination filled in, and opens the pull request in two clicks', async () => {
  useStore.setState({ workspaces: { [workspace.id]: { ...workspace, origin } } })
  const target = { repository: 'upstream/project', base_branch: 'main', head_repository: 'fork/project', head_branch: status.branch }
  const client = fakeApi({
    runGitStatus: vi.fn(async () => committed),
    workspaceMirrorStatus: vi.fn(async () => ({ enabled: true, source_url: 'https://github.com/upstream/project.git' })),
    runGitPush: vi.fn(async () => ({ pushed: true, head: status.head, target: { remote: 'origin', repository: origin, head_branch: status.branch }, output })),
    runPRStatus: vi.fn(async () => ({ identity: status.identity, account_member_id: status.account_member_id, pull_request: null, output })),
    runPRCreate: vi.fn(async () => ({
      identity: status.identity, account_member_id: status.account_member_id, pull_request: pullRequest,
      created: true, reconciled: false, creation_uncertain: false, output,
    })),
  })
  renderOpen(client)

  const pr = within(await screen.findByRole('region', { name: 'GitHub pull request' }))
  expect(screen.getByRole('tab', { name: '2 · Push and pull request' }).getAttribute('aria-selected')).toBe('true')
  expect(screen.getByRole('combobox', { name: 'Push remote' }).textContent).toBe('origin')
  expect(screen.getByRole('combobox', { name: 'Writable push URL' }).textContent).toBe(origin)
  expect(screen.getByLabelText('Push head branch')).toHaveProperty('value', status.branch)
  expect(pr.getByLabelText('PR repository (owner/name)')).toHaveProperty('value', 'upstream/project')
  expect(pr.getByLabelText('PR base branch')).toHaveProperty('value', 'main')
  expect(pr.getByLabelText('PR head repository (owner/name)')).toHaveProperty('value', 'fork/project')
  expect(pr.getByLabelText('PR head branch')).toHaveProperty('value', status.branch)
  expect(pr.getByLabelText('PR title')).toHaveProperty('value', 'fix the flaky scheduler test')
  expect(await pr.findByText('No existing PR for this exact repository, base and head.')).toBeTruthy()
  expect(client.runPRStatus).toHaveBeenCalledWith({ run_id: run().id, expected: { branch: status.branch, head: status.head }, target })

  const push = screen.getByRole('button', { name: 'Push branch' })
  expect(push.parentElement!.previousElementSibling!.textContent).toBe(`Pushes commit aaaaaaaa to ${origin} as branch ${status.branch}. Never a force push.`)
  fireEvent.click(push)
  expect(await screen.findByRole('region', { name: 'Push outcome' })).toHaveProperty('textContent', expect.stringContaining('Pushed: yes'))
  expect(client.runGitPush).toHaveBeenCalledWith({
    run_id: run().id, expected: { branch: status.branch, head: status.head }, target: { remote: 'origin', repository: origin, head_branch: status.branch },
  })

  const create = pr.getByRole('button', { name: 'Create pull request' })
  await waitFor(() => expect(create).toHaveProperty('disabled', false))
  expect(create.parentElement!.previousElementSibling!.textContent).toBe(
    `Opens a pull request into upstream/project:main from fork/project:${status.branch} as reviewed-login. It is not merged from here.`,
  )
  fireEvent.click(create)
  expect(await pr.findByRole('link', { name: '#7: fix the flaky scheduler test' })).toHaveProperty('href', pullRequest.url)
  expect(client.runPRCreate).toHaveBeenCalledWith({
    run_id: run().id, expected: { branch: status.branch, head: status.head }, target,
    expected_login: 'reviewed-login', title: 'fix the flaky scheduler test', body: '', draft: false,
  })
  expect(pr.queryByRole('button', { name: 'Create pull request' })).toBeNull()
})

it('shows the pull request a run already has in place of the form to create one', async () => {
  useStore.setState({ workspaces: { [workspace.id]: { ...workspace, origin } } })
  const client = fakeApi({
    runGitStatus: vi.fn(async () => committed),
    runPRStatus: vi.fn(async () => ({
      identity: status.identity, account_member_id: status.account_member_id, output,
      pull_request: { ...pullRequest, repository: 'fork/project', url: 'https://github.com/fork/project/pull/7' },
    })),
  })
  renderOpen(client)

  const pr = within(await screen.findByRole('region', { name: 'GitHub pull request' }))
  expect(await pr.findByRole('link', { name: '#7: fix the flaky scheduler test' })).toBeTruthy()
  expect(pr.getByLabelText('PR repository (owner/name)')).toHaveProperty('value', 'fork/project')
  expect(pr.queryByLabelText('PR title')).toBeNull()
  expect(pr.queryByRole('button', { name: 'Create pull request' })).toBeNull()
})

it('chooses no destination for a workspace without an Origin, and pushes only to the exact URL picked', async () => {
  const server = '/srv/aether/project.git'
  const client = fakeApi({
    runGitStatus: vi.fn(async () => ({ ...status, remotes: [...status.remotes!, { name: 'origin', fetch_urls: [server], push_urls: [server] }] })),
    runGitPush: vi.fn(async () => { throw new Error('run.git.push: remote rejected (protected branch hook declined)') }),
  })
  renderOpen(client)
  await screen.findByRole('checkbox', { name: 'Select selected.txt' })
  step('2 · Push and pull request')

  const push = screen.getByRole('button', { name: 'Push branch' })
  expect(push).toHaveProperty('disabled', true)
  expect(push.parentElement!.previousElementSibling!.textContent).toContain('This workspace sets no checkout Origin')
  await pickOption(screen.getByRole('combobox', { name: 'Push remote' }), 'fork')
  await waitFor(() => expect(screen.getByRole('combobox', { name: 'Writable push URL' }).textContent).toBe('https://github.com/fork/project.git'))
  fireEvent.change(screen.getByLabelText('Push head branch'), { target: { value: 'reviewed' } })
  await waitFor(() => expect(push).toHaveProperty('disabled', false))
  fireEvent.click(push)

  const refused = await screen.findByText('run.git.push: remote rejected (protected branch hook declined)')
  expect(push.parentElement!.contains(refused)).toBe(true)
  expect(client.runGitPush).toHaveBeenCalledWith({
    run_id: run().id, expected: { branch: status.branch, head: status.head },
    target: { remote: 'fork', repository: 'https://github.com/fork/project.git', head_branch: 'reviewed' },
  })
  expect(client.runPRStatus).not.toHaveBeenCalled()
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
  expect(client.runPRStatus).not.toHaveBeenCalled()
  fireEvent.click(pr.getByRole('button', { name: 'Discover existing PR' }))
  fireEvent.change(pr.getByLabelText('PR title'), { target: { value: 'reviewed change' } })
  const create = pr.getByRole('button', { name: 'Create pull request' })
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
  expect(again.getByRole('button', { name: 'Create pull request' })).toHaveProperty('disabled', true)
  fireEvent.click(again.getByRole('button', { name: 'Reconcile PR read-only' }))
  await waitFor(() => expect(again.getByRole('button', { name: 'Reconcile PR read-only' })).toHaveProperty('disabled', false))
  expect(again.getByRole('button', { name: 'Create pull request' })).toHaveProperty('disabled', true)
  expect(again.getByLabelText('PR head branch')).toHaveProperty('value', 'reviewed')
  expect(client.runGitStatus).toHaveBeenCalledTimes(1)
  expect(client.runPRCreate).toHaveBeenCalledTimes(1)
})
