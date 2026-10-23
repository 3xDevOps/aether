import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { routeFromQuery, routeQuery } from '@/lib/url-state'
import { ChangesView } from '@/routes/diff'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { initialDiff } from '@/store/diff'
import { toRecord } from '@/store/runs'
import { alice, run, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const docs = { ...workspace, id: 'wsp_docs', name: 'docs-site' }
const added = (path: string) => `diff --git a/${path} b/${path}\nnew file mode 100644\n--- /dev/null\n+++ b/${path}\n@@ -0,0 +1 @@\n+hello\n`
const primaryPatch = added('cmd/main.go')
const docsPatch = added('guide.md') + added('index.md')

const multi = run({
  id: 'run_multi',
  base_branch: 'main',
  repos: [
    { workspace_id: workspace.id, name: workspace.name, path: '/workspace', branch: 'aether/run-multi', primary: true },
    { workspace_id: docs.id, name: docs.name, path: '/repos/docs-site', branch: 'aether/run-multi-docs', primary: false },
  ],
})

const agent = { localControl: true, steerable: true, controlUnavailable: false } as AgentTerminal

function seed(params: Record<string, string> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace, [docs.id]: docs },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
    runs: { [multi.id]: toRecord(multi) },
    diffs: { [multi.id]: { ...initialDiff, status: 'ready', base: 'abc', patch: primaryPatch, revision: 0, fetched: 0 } },
    route: { name: 'run', params: { runId: multi.id, view: 'changes', ...params } },
    hydrated: true,
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.workspaceTimeline).mockResolvedValue({ events: [], next_seq: 0, more: false })
  vi.mocked(api.runPatch).mockImplementation(async (runID, _range, repo) => ({
    run_id: runID, base: 'basesha0', patch: repo === docs.id ? docsPatch : primaryPatch, truncated: false,
  }))
})

const renderDiff = () => render(<ChangesView runID={multi.id} agent={agent} agentName="Claude Code" />)

it('shows no repo selector for a single-repo run', () => {
  seed()
  useStore.setState({ runs: { [multi.id]: toRecord({ ...multi, repos: undefined }) } })
  renderDiff()
  expect(screen.queryByRole('tablist', { name: 'Repos' })).toBeNull()
})

it('lists the repos with their file counts and reads a repo\'s changes through the repo param', async () => {
  seed()
  renderDiff()

  const tabs = within(screen.getByRole('tablist', { name: 'Repos' })).getAllByRole('tab')
  expect(tabs.map((tab) => tab.getAttribute('data-state'))).toEqual(['active', 'inactive', 'inactive'])
  await waitFor(() => expect(tabs[1]!.textContent).toBe('docs-site2'))
  expect(api.runPatch).toHaveBeenCalledWith(multi.id, undefined, docs.id)
  expect(await screen.findByRole('region', { name: 'cmd/main.go' })).toBeTruthy()

  fireEvent.mouseDown(tabs[1]!)
  expect(useStore.getState().route.params).toEqual({ runId: multi.id, view: 'changes', repo: docs.id })
  expect(await screen.findByRole('region', { name: 'guide.md' })).toBeTruthy()
  expect(screen.queryByRole('region', { name: 'cmd/main.go' })).toBeNull()
})

it('opens Publish for the repo, naming it', async () => {
  seed({ repo: docs.id })
  renderDiff()
  fireEvent.click(await screen.findByRole('button', { name: 'Publish…' }))
  expect(await screen.findByRole('dialog', { name: 'Publish docs-site' })).toBeTruthy()
  await waitFor(() => expect(api.runGitStatus).toHaveBeenCalledWith({ run_id: multi.id, repo: docs.id }))
})

it('groups every repo\'s changes under All repos and opens a file in its repo', async () => {
  seed({ repo: 'all' })
  renderDiff()

  const docsSection = within(await screen.findByRole('region', { name: 'docs-site' }))
  await docsSection.findByText('2 files')
  expect(within(screen.getByRole('region', { name: 'main-repo' })).getByText('1 file')).toBeTruthy()
  fireEvent.click(docsSection.getByRole('button', { name: /guide\.md/ }))
  expect(useStore.getState().route.params).toEqual({ runId: multi.id, view: 'changes', file: 'guide.md', repo: docs.id })
})

it('keeps the selected repo in the address', () => {
  const route = { name: 'run', params: { runId: multi.id, view: 'changes', repo: docs.id } }
  expect(routeQuery(route).toString()).toBe(`run=${multi.id}&view=changes&repo=${docs.id}`)
  expect(routeFromQuery(routeQuery(route))).toEqual(route)
})

it('merges the repo\'s pull request through the repo param', async () => {
  const output = { exit_code: 0, truncated: false }
  const head = 'b'.repeat(40)
  vi.mocked(api.runGitStatus).mockResolvedValue({
    branch: 'aether/run-multi-docs', head, detached: false, unborn: false, changes: [], truncated: false,
    account_member_id: serverInfo.member.id, account_name: 'Alice', identity: 'alice',
    remotes: [{ name: 'origin', fetch_urls: ['git@github.com:acme/docs.git'], push_urls: ['git@github.com:acme/docs.git'] }], output,
  })
  vi.mocked(api.runPRFeedback).mockResolvedValue({
    identity: 'alice', account_member_id: serverInfo.member.id, checks: [], comments: [], reviews: [], review_comments: [], truncated: false, output,
    pull_request: {
      number: 4, url: 'https://github.com/acme/docs/pull/4', state: 'open', title: 'Docs', draft: false, repository: 'acme/docs',
      base_branch: 'main', head_repository: 'acme/docs', head_branch: 'aether/run-multi-docs', head_oid: head,
    },
  })
  vi.mocked(api.runPRMerge).mockResolvedValue({ merged: true, output } as Awaited<ReturnType<typeof api.runPRMerge>>)
  seed({ repo: docs.id })
  useStore.setState({ workspaces: { [workspace.id]: workspace, [docs.id]: { ...docs, origin: 'git@github.com:acme/docs.git' } } })
  renderDiff()

  fireEvent.click(await screen.findByRole('button', { name: 'Merge' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Squash and merge' }))
  await waitFor(() => expect(api.runPRMerge).toHaveBeenCalledWith(expect.objectContaining({
    run_id: multi.id, repo: docs.id, method: 'squash', repository: 'acme/docs', number: 4, expected_head: head, expected_base: 'main',
  })))
})
