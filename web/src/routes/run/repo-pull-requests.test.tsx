import { fireEvent, render, screen, within } from '@testing-library/react'
import { api } from '@/lib/api'
import type { RunGitStatusResult, RunPRFeedbackResult, RunPullRequest } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useRunPullRequest } from '@/routes/run/pull-request'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { run, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const docs = { ...workspace, id: 'wsp_docs', name: 'docs-site' }
const output = { exit_code: 0, truncated: false }

function status(origin: string, branch: string): RunGitStatusResult {
  return {
    branch, head: 'a'.repeat(40), detached: false, unborn: false, changes: [], truncated: false,
    account_member_id: serverInfo.member.id, account_name: 'Alice', identity: 'alice',
    remotes: [{ name: 'origin', fetch_urls: [origin], push_urls: [origin] }], output,
  }
}

function feedback(repository: string, number: number, checks: RunPRFeedbackResult['checks']): RunPRFeedbackResult {
  const pull_request: RunPullRequest = {
    number, url: `https://github.com/${repository}/pull/${number}`, state: 'open', title: 'Change', draft: false,
    repository, base_branch: 'main', head_repository: repository, head_branch: 'aether/run-multi', head_oid: 'a'.repeat(40),
  }
  return { identity: 'alice', account_member_id: serverInfo.member.id, pull_request, checks, comments: [], reviews: [], review_comments: [], truncated: false, output }
}

const agent = { localControl: true, steerable: true, controlUnavailable: false } as AgentTerminal

function Element({ record }: { record: ReturnType<typeof toRecord> }) {
  const pullRequest = useRunPullRequest(record, agent, 'Claude Code')
  return <>{pullRequest.status}{pullRequest.panel}</>
}

function renderMulti() {
  const record = toRecord(run({
    id: 'run_multi_pr',
    repos: [
      { workspace_id: workspace.id, name: workspace.name, path: '/workspace', branch: 'aether/run-multi', primary: true },
      { workspace_id: docs.id, name: docs.name, path: '/repos/docs-site', branch: 'aether/run-multi', primary: false },
    ],
  }))
  useStore.setState({ runs: { [record.id]: record } })
  render(<Element record={record} />)
  return record
}

beforeEach(() => {
  vi.clearAllMocks()
  useStore.setState({
    info: serverInfo, capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    workspaces: {
      [workspace.id]: { ...workspace, origin: 'git@github.com:acme/app.git' },
      [docs.id]: { ...docs, origin: 'git@github.com:acme/docs.git' },
    },
  })
  vi.mocked(api.runGitStatus).mockImplementation(async ({ repo }) =>
    repo ? status('git@github.com:acme/docs.git', 'aether/run-multi') : status('git@github.com:acme/app.git', 'aether/run-multi'))
  vi.mocked(api.runPRFeedback).mockImplementation(async ({ repo }) =>
    repo
      ? feedback('acme/docs', 4, [{ name: 'lint', status: 'completed', conclusion: 'failure' }])
      : feedback('acme/app', 9, [{ name: 'build', status: 'completed', conclusion: 'success' }]))
})

it('summarises every repo\'s pull request as one chip and lists them in a popover', async () => {
  const record = renderMulti()

  const chip = await screen.findByRole('button', { name: '2 PRs' })
  expect(within(screen.getByRole('group', { name: 'Pull requests' })).getByRole('button')).toBe(chip)
  expect(api.runPRFeedback).toHaveBeenCalledWith(expect.objectContaining({ run_id: record.id, repo: docs.id }))

  fireEvent.click(chip)
  const list = within(await screen.findByRole('dialog', { name: 'Pull requests by repo' }))
  expect(list.getByRole('link', { name: 'Pull request #9 in main-repo on GitHub' })).toHaveProperty('href', 'https://github.com/acme/app/pull/9')
  expect(list.getByRole('link', { name: 'Pull request #4 in docs-site on GitHub' })).toHaveProperty('href', 'https://github.com/acme/docs/pull/4')
  expect(list.getByText(/Checks passed/)).toBeTruthy()
  expect(list.getByText(/1 failing/)).toBeTruthy()
})

it('opens the usual feedback panel for the repo chosen', async () => {
  renderMulti()
  fireEvent.click(await screen.findByRole('button', { name: '2 PRs' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Checks and review comments for docs-site' }))

  const panel = within(await screen.findByRole('dialog', { name: 'Pull request #4' }))
  expect(within(panel.getByRole('region', { name: 'Checks' })).getAllByRole('listitem')[0]!.textContent).toContain('lint · failure')
})

it('keeps the single-PR indicator for a single-repo run', async () => {
  const record = toRecord(run({ id: 'run_single_pr' }))
  useStore.setState({ runs: { [record.id]: record } })
  render(<Element record={record} />)
  expect(await screen.findByRole('group', { name: 'Pull request' })).toBeTruthy()
  expect(screen.queryByRole('group', { name: 'Pull requests' })).toBeNull()
})
