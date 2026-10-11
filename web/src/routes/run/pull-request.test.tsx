import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import type { RunGitStatusResult, RunPRCheck, RunPRFeedbackResult, RunPullRequest as PullRequest } from '@/lib/types'
import { refreshPullRequest } from '@/routes/diff/publish-pr'
import { githubRepository } from '@/routes/diff/publish-target'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { RunPullRequest } from '@/routes/run/pull-request'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { bob, run, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const output = { exit_code: 0, truncated: false }
const origin = 'git@github.com:acme/app.git'
const status: RunGitStatusResult = {
  branch: 'aether/run-1-checkout', head: 'a'.repeat(40), detached: false, unborn: false, changes: [], truncated: false,
  account_member_id: serverInfo.member.id, account_name: 'Alice', identity: 'alice',
  remotes: [{ name: 'origin', fetch_urls: [origin], push_urls: [origin] }], output,
}
const pullRequest: PullRequest = {
  number: 7, url: 'https://github.com/acme/app/pull/7', state: 'open', title: 'Retry uploads', draft: false,
  repository: 'acme/app', base_branch: 'main', head_repository: 'acme/app', head_branch: status.branch, head_oid: status.head,
}
const lint: RunPRCheck = { name: 'lint', status: 'completed', conclusion: 'failure', url: 'https://github.com/acme/app/actions/runs/1/job/2' }

function feedback(over: Partial<RunPRFeedbackResult> = {}): RunPRFeedbackResult {
  return {
    identity: 'alice', account_member_id: serverInfo.member.id, pull_request: pullRequest, checks: [], comments: [], reviews: [], review_comments: [],
    truncated: false, output, ...over,
  }
}

const agent = {
  localControl: true,
  roomControl: { has_control: true, control_session_id: 'ctl', control_generation: 3 },
  steerable: true,
  controlUnavailable: false,
} as AgentTerminal

let sequence = 0
function renderFor(over: Parameters<typeof run>[0] = {}) {
  const record = toRecord(run({ id: `run_pr_${++sequence}`, ...over }))
  useStore.setState({ runs: { [record.id]: record } })
  render(<RunPullRequest run={record} agent={agent} agentName="Claude Code" />)
  return record
}

beforeEach(() => {
  vi.clearAllMocks()
  useStore.setState({
    info: serverInfo, capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    workspaces: { [workspace.id]: { ...workspace, origin } },
  })
  vi.mocked(api.runGitStatus).mockResolvedValue(status)
})

it('shows the pull request with its failing checks, and sends one check or one comment to the agent', async () => {
  vi.mocked(api.runPRFeedback).mockResolvedValue(feedback({
    checks: [
      { name: 'docs', status: 'completed', conclusion: 'success' },
      { name: 'build', status: 'in_progress' },
      lint,
      { name: 'ci/legacy', status: 'COMPLETED', conclusion: 'success' },
    ],
    review_comments: [{
      id: 'r1', review_id: 'v1', author: 'bob', body: 'Cap the retries.', url: 'https://github.com/acme/app/pull/7#discussion_r1',
      created_at: '2026-08-14T10:03:00Z', path: 'upload.go', line: 42, side: 'RIGHT', commit_oid: status.head,
    }],
  }))
  const record = renderFor()

  const element = within(await screen.findByRole('group', { name: 'Pull request' }))
  expect(element.getByRole('link', { name: 'Pull request #7 on GitHub' })).toHaveProperty('href', pullRequest.url)
  expect(api.runPRFeedback).toHaveBeenCalledWith({
    run_id: record.id, expected: { branch: status.branch, head: status.head }, limit: 100,
    target: { repository: 'acme/app', base_branch: 'main', head_repository: 'acme/app', head_branch: status.branch },
  })
  fireEvent.click(element.getByRole('button', { name: '1 failing' }))

  const panel = within(await screen.findByRole('dialog', { name: 'Pull request #7' }))
  const checks = within(panel.getByRole('region', { name: 'Checks' }))
  expect(checks.getByText('1 failing · 1 running · 2 passed')).toBeTruthy()
  expect(checks.getAllByRole('listitem').map((item) => item.textContent)).toEqual(['lint · failureSend to agent', 'build · in progress'])
  fireEvent.click(checks.getByRole('button', { name: 'Send to agent' }))
  expect(await checks.findByRole('status')).toHaveProperty('textContent', 'Sent 1 check to Claude Code. Open the session')
  expect(vi.mocked(api.runRoomPost).mock.calls[0][0]).toMatchObject({
    run_id: record.id, kind: 'steer_request', control_session_id: 'ctl', control_generation: 3,
    body: `GitHub feedback on acme/app#7 (${pullRequest.url}), acme/app:${status.branch} → main, head ${status.head}:\n\nFailing check: lint\nConclusion: failure\n${lint.url}`,
  })

  const comments = within(panel.getByRole('region', { name: 'Review comments' }))
  expect(comments.getByText('upload.go:42')).toBeTruthy()
  fireEvent.click(comments.getByRole('button', { name: 'Send to agent' }))
  await comments.findByRole('status')
  expect(vi.mocked(api.runRoomPost).mock.calls[1][0].body).toContain('Inline review by bob: upload.go:42 (RIGHT)\nCap the retries.')
})

it.each([
  [[{ name: 'build', status: 'queued' }, { name: 'docs', status: 'completed', conclusion: 'success' }], 'Checks running'],
  [[{ name: 'docs', status: 'completed', conclusion: 'skipped' }], 'Checks passed'],
  [[], 'No checks'],
])('summarises checks %#', async (checks, label) => {
  vi.mocked(api.runPRFeedback).mockResolvedValue(feedback({ checks }))
  renderFor()
  expect(await screen.findByRole('button', { name: label })).toBeTruthy()
})

it('keeps the server’s error on the row whose send it refused', async () => {
  vi.mocked(api.runPRFeedback).mockResolvedValue(feedback({ checks: [lint] }))
  vi.mocked(api.runRoomPost).mockRejectedValueOnce(new Error('run.room.post: run is not accepting messages'))
  renderFor()
  fireEvent.click(await screen.findByRole('button', { name: '1 failing' }))
  const panel = within(await screen.findByRole('dialog'))
  fireEvent.click(panel.getByRole('button', { name: 'Send to agent' }))
  expect((await panel.findByRole('alert')).textContent).toBe('run.room.post: run is not accepting messages')
  expect(panel.getByRole('button', { name: 'Send to agent' })).toHaveProperty('disabled', false)
})

it('says why nothing can be sent to a run the member may not message', async () => {
  useStore.setState({ info: { ...serverInfo, member: bob } })
  vi.mocked(api.runPRFeedback).mockResolvedValue(feedback({ checks: [lint] }))
  renderFor({ protected: true })
  fireEvent.click(await screen.findByRole('button', { name: '1 failing' }))
  const panel = within(await screen.findByRole('dialog'))
  expect(panel.getByText('This run is protected: only its owner or an admin can message the agent.')).toBeTruthy()
  expect(panel.getByRole('button', { name: 'Send to agent' })).toHaveProperty('disabled', true)
})

it('shows nothing without a pull request, and reads nothing once the run has finished', async () => {
  vi.mocked(api.runPRFeedback).mockResolvedValue(feedback({ pull_request: null }))
  renderFor()
  await waitFor(() => expect(api.runPRFeedback).toHaveBeenCalledTimes(1))
  expect(screen.queryByRole('group', { name: 'Pull request' })).toBeNull()

  vi.clearAllMocks()
  renderFor({ status: 'merged' })
  await Promise.resolve()
  expect(api.runGitStatus).not.toHaveBeenCalled()
})

it('asks again from the checkout once its branch or HEAD has moved', async () => {
  const record = toRecord(run({ id: 'run_pr_moved' }))
  vi.mocked(api.runPRFeedback).mockResolvedValue(feedback())
  await refreshPullRequest(record)
  await refreshPullRequest(record)
  expect(api.runGitStatus).toHaveBeenCalledTimes(1)

  const moved = { branch: 'feat/retry', head: 'b'.repeat(40) }
  vi.mocked(api.runGitStatus).mockResolvedValue({ ...status, ...moved })
  vi.mocked(api.runPRFeedback).mockResolvedValueOnce(feedback({ pull_request: null, error: 'runrepo: branch or HEAD changed; refresh before acting', actual: moved }))
  await refreshPullRequest(record)
  expect(api.runGitStatus).toHaveBeenCalledTimes(2)
  expect(vi.mocked(api.runPRFeedback).mock.lastCall![0]).toMatchObject({ expected: moved, target: { head_branch: 'feat/retry' } })
})

it('reads owner/name from the GitHub remote forms git accepts, and from nothing else', () => {
  expect([
    'https://github.com/acme/app.git', 'https://github.com/acme/app', 'https://user@github.com/acme/app.git/',
    'git@github.com:acme/app.git', 'ssh://git@github.com/acme/app',
  ].map(githubRepository)).toEqual(Array(5).fill('acme/app'))
  expect(['https://gitlab.com/acme/app.git', '/srv/aether/app.git', 'https://github.com/acme', undefined].map(githubRepository)).toEqual(['', '', '', ''])
})

it('reserves a send while control is on its way, and frees the rows when it never arrives', async () => {
  vi.mocked(api.runPRFeedback).mockResolvedValue(feedback({ checks: [lint, { ...lint, name: 'test' }] }))
  const waiting = { ...agent, localControl: false, roomControl: undefined, session: { takeControl: vi.fn() } } as unknown as AgentTerminal
  const record = toRecord(run({ id: 'run_pr_control' }))
  useStore.setState({ runs: { [record.id]: record } })
  const view = render(<RunPullRequest run={record} agent={waiting} agentName="Claude Code" />)
  fireEvent.click(await screen.findByRole('button', { name: '2 failing' }))
  const panel = within(await screen.findByRole('dialog'))

  vi.useFakeTimers()
  try {
    const [first, second] = panel.getAllByRole('button', { name: 'Send to agent' })
    fireEvent.click(first)
    expect(waiting.session.takeControl).toHaveBeenCalledTimes(1)
    expect(second).toHaveProperty('disabled', true)
    act(() => void vi.advanceTimersByTime(10_500))
    expect(panel.getByRole('alert').textContent).toBe('Control of the run did not arrive, so nothing was sent.')
    expect(api.runRoomPost).not.toHaveBeenCalled()

    fireEvent.click(panel.getAllByRole('button', { name: 'Send to agent' })[1])
    view.rerender(<RunPullRequest run={record} agent={agent} agentName="Claude Code" />)
    await act(async () => void (await vi.advanceTimersByTimeAsync(0)))
  } finally {
    vi.useRealTimers()
  }
  expect(vi.mocked(api.runRoomPost).mock.calls.map(([request]) => request.body.split('\n')[2])).toEqual(['Failing check: test'])
})
