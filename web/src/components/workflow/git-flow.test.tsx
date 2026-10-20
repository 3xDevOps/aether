import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { GitFlow } from '@/components/workflow/git-flow'
import { api } from '@/lib/api'
import type { RunGitLogResult, RunGitStatusResult, RunPRFeedbackResult, RunPullRequest } from '@/lib/types'
import { RunAgentContext } from '@/routes/run/agent-context'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { bob, run, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const output = { exit_code: 0, truncated: false }
const origin = 'git@github.com:acme/app.git'
const head = 'a'.repeat(40)
const status: RunGitStatusResult = {
  branch: 'aether/run-1', head, detached: false, unborn: false, changes: [], truncated: false,
  account_member_id: serverInfo.member.id, account_name: 'Alice', identity: 'alice',
  remotes: [{ name: 'origin', fetch_urls: [origin], push_urls: [origin] }], output,
}
const pr: RunPullRequest = {
  number: 128, url: 'https://github.com/acme/app/pull/128', state: 'open', title: 'Retry uploads', draft: false,
  repository: 'acme/app', base_branch: 'main', head_repository: 'acme/app', head_branch: status.branch, head_oid: head,
}

function log(over: Partial<RunGitLogResult> = {}): RunGitLogResult {
  return {
    branch: status.branch, base_ref: 'main', base: 'b'.repeat(40), head, merge_base: 'b'.repeat(40), ahead: 1, behind: 0,
    commits: [{ sha: head, short: head.slice(0, 7), subject: 'Retry failed uploads', author: 'Ada', authored_at: '2026-10-20T10:00:00Z', parents: ['b'.repeat(40)] }],
    upstream: [], conflicts: [], truncated: false, output, ...over,
  }
}

function feedback(over: Partial<RunPRFeedbackResult> = {}): RunPRFeedbackResult {
  return {
    identity: 'alice', account_member_id: serverInfo.member.id, pull_request: pr,
    checks: [{ name: 'test', status: 'completed', conclusion: 'success', url: 'https://github.com/acme/app/actions/runs/1' }],
    comments: [], reviews: [{ id: 'r1', author: 'bob', body: '', state: 'APPROVED', submitted_at: '2026-10-20T10:05:00Z' }],
    review_comments: [], truncated: false, output, ...over,
  }
}

const agent = {
  localControl: true,
  roomControl: { has_control: true, control_session_id: 'ctl', control_generation: 3 },
  steerable: true,
  controlUnavailable: false,
} as AgentTerminal

let sequence = 0
function renderCard(over: Parameters<typeof run>[0] = {}) {
  const record = toRecord(run({ id: `run_git_${++sequence}`, ...over }))
  useStore.setState({ runs: { [record.id]: record } })
  render(
    <RunAgentContext value={{ agent, agentName: 'Claude Code' }}>
      <GitFlow runId={record.id} />
    </RunAgentContext>,
  )
  return record
}

beforeEach(() => {
  vi.clearAllMocks()
  useStore.setState({
    info: serverInfo, capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    workspaces: { [workspace.id]: { ...workspace, origin } },
  })
  vi.mocked(api.runGitStatus).mockResolvedValue(status)
  vi.mocked(api.runPRFeedback).mockResolvedValue(feedback())
  vi.mocked(api.runGitLog).mockResolvedValue(log())
})

it('reaches the merge, its method and the confirm by keyboard, and shows GitHub’s refusal verbatim', async () => {
  const refusal = 'GraphQL: Pull request is not mergeable: the merge commit cannot be cleanly created. (mergePullRequest)'
  vi.mocked(api.runPRMerge).mockResolvedValue({ pull_request: pr, merged: false, account_member_id: serverInfo.member.id, output, error: refusal })
  const user = userEvent.setup()
  const record = renderCard()
  const card = within(await screen.findByRole('region', { name: 'Branch and pull request' }))
  expect(await card.findByText('Ready to merge')).toBeTruthy()
  expect(card.getByText('Merge into main')).toBeTruthy()
  expect(card.getByRole('link', { name: /test\s*passed/ })).toHaveProperty('href', 'https://github.com/acme/app/actions/runs/1')

  const merge = card.getByRole('button', { name: 'Squash and merge' })
  const method = card.getByRole('button', { name: 'Merge method' })
  merge.focus()
  await user.tab()
  expect(document.activeElement).toBe(method)
  await user.keyboard('{Enter}')
  await user.click(await screen.findByRole('menuitemradio', { name: 'Rebase and merge' }))
  expect(card.getByRole('button', { name: 'Rebase and merge' })).toBeTruthy()

  card.getByRole('button', { name: 'Rebase and merge' }).focus()
  await user.keyboard('{Enter}')
  const confirm = within(await screen.findByRole('alertdialog', { name: 'Rebase and merge #128?' }))
  await user.click(confirm.getByRole('button', { name: 'Rebase and merge' }))
  expect(api.runPRMerge).toHaveBeenCalledWith({ run_id: record.id, method: 'rebase', repository: 'acme/app', number: 128 })
  expect((await confirm.findByRole('alert')).textContent).toBe(refusal)
  await waitFor(() => expect(card.getAllByRole('alert').map((entry) => entry.textContent)).toContain(refusal))
})

it('asks the agent to resolve conflicts, naming the base and each file', async () => {
  vi.mocked(api.runGitLog).mockResolvedValue(log({ behind: 1, conflicts: [{ path: 'upload.go' }, { path: 'web/retry.ts' }] }))
  const user = userEvent.setup()
  const record = renderCard()
  const card = within(await screen.findByRole('region', { name: 'Branch and pull request' }))
  await user.click(await card.findByRole('button', { name: '2 conflicts' }))
  const files = await screen.findByText('web/retry.ts')
  expect(files).toBeTruthy()
  await user.keyboard('{Escape}')

  await user.click(card.getByRole('button', { name: 'Ask agent to resolve conflicts' }))
  await waitFor(() => expect(api.runRoomPost).toHaveBeenCalled())
  expect(vi.mocked(api.runRoomPost).mock.calls[0]![0]).toMatchObject({ run_id: record.id, kind: 'steer_request' })
  expect(vi.mocked(api.runRoomPost).mock.calls[0]![0].body).toContain('Merging main (bbbbbbbbbbbb) into aether/run-1 conflicts in these files:\n- upload.go\n- web/retry.ts')
  expect(await card.findByText('Sent the conflict list to Claude Code.')).toBeTruthy()
})

it('offers a viewer no merge and no send, only the pull request on GitHub', async () => {
  useStore.setState({ info: { ...serverInfo, member: { ...bob, role: 'viewer' } } })
  vi.mocked(api.runGitLog).mockResolvedValue(log({ conflicts: [{ path: 'upload.go' }] }))
  renderCard()
  const card = within(await screen.findByRole('region', { name: 'Branch and pull request' }))
  expect(await card.findByRole('link', { name: 'Open on GitHub' })).toHaveProperty('href', pr.url)
  expect(card.queryByRole('button', { name: 'Squash and merge' })).toBeNull()
  expect(card.queryByRole('button', { name: 'Ask agent to resolve conflicts' })).toBeNull()
})

it('shows the server’s error for the branch verbatim', async () => {
  vi.mocked(api.runGitLog).mockRejectedValue(new Error('run.git.log: fatal: ambiguous argument \'main\': unknown revision'))
  renderCard()
  const card = within(await screen.findByRole('region', { name: 'Branch and pull request' }))
  expect((await card.findByRole('alert')).textContent).toBe('run.git.log: fatal: ambiguous argument \'main\': unknown revision')
  expect(card.getAllByText('Not checked yet').length).toBeGreaterThan(0)
})
