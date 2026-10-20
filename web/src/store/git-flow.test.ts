import type { GitLogCommit, RunGitLogResult, RunPRFeedbackResult, RunPullRequest } from '@/lib/types'
import { chooseAction, headRow, layoutGraph, readiness, resolvePrompt, type Permissions } from '@/store/git-flow'

const output = { exit_code: 0, truncated: false }

function commit(sha: string, parents: string[], at: string, over: Partial<GitLogCommit> = {}): GitLogCommit {
  return { sha, short: sha.slice(0, 7), subject: `subject ${sha}`, author: 'ada', authored_at: at, parents, ...over }
}

function log(over: Partial<RunGitLogResult> = {}): RunGitLogResult {
  return {
    branch: 'aether/run-1', base_ref: 'main', base: 'base0', head: 'own2', merge_base: 'base0', ahead: 2, behind: 0,
    commits: [commit('own2', ['own1'], '2026-10-20T10:02:00Z'), commit('own1', ['base0'], '2026-10-20T10:01:00Z')],
    upstream: [], conflicts: [], truncated: false, output, ...over,
  }
}

const pr: RunPullRequest = {
  number: 128, url: 'https://github.com/acme/app/pull/128', state: 'open', title: 'Retry uploads', draft: false,
  repository: 'acme/app', base_branch: 'main', head_repository: 'acme/app', head_branch: 'aether/run-1', head_oid: 'own2',
}

function feedback(over: Partial<RunPRFeedbackResult> = {}): RunPRFeedbackResult {
  return {
    identity: 'ada', account_member_id: 'm1', pull_request: pr, checks: [{ name: 'test', status: 'completed', conclusion: 'success' }],
    comments: [], reviews: [{ id: 'r1', author: 'bob', body: '', state: 'APPROVED', submitted_at: '2026-10-20T10:05:00Z' }],
    review_comments: [], mergeable: 'MERGEABLE', merge_state_status: 'CLEAN', truncated: false, output, ...over,
  }
}

const everyone: Permissions = { publish: true, create: true, merge: true, prompt: true }

describe('layoutGraph', () => {
  it('draws a linear branch on its own lane, bending into the merge base', () => {
    const graph = layoutGraph(log())
    expect(graph.rows.map((row) => row.kind)).toEqual(['head', 'commit', 'commit', 'base'])
    expect(graph.lanes).toBe(2)
    const tip = graph.rows[1]!
    expect(tip.kind === 'commit' && tip.tip).toBe('aether/run-1')
    expect(graph.edges).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 0, to: 1, fromLane: 0, toLane: 0, style: 'dashed', own: true }),
      expect.objectContaining({ from: 1, to: 2, fromLane: 0, toLane: 0, style: 'solid' }),
      expect.objectContaining({ from: 2, to: 3, fromLane: 0, toLane: 1, bend: 'late', style: 'solid' }),
    ]))
    expect(graph.edges).toHaveLength(3)
  })

  it('draws a merge commit as a curve that leaves its row into the base lane', () => {
    const graph = layoutGraph(log({
      commits: [commit('own2', ['own1', 'base0'], '2026-10-20T10:02:00Z'), commit('own1', ['old'], '2026-10-20T10:01:00Z')],
    }))
    expect(graph.edges).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 1, fromLane: 0, to: 3, toLane: 1, bend: 'early', own: true }),
      expect.objectContaining({ from: 2, fromLane: 0, to: 3, toLane: 1, bend: 'late' }),
    ]))
  })

  it('puts upstream commits on the muted base lane, interleaved by time, with a dashed edge from the pending action', () => {
    const graph = layoutGraph(log({
      behind: 1, base: 'up1',
      upstream: [commit('up1', ['base0'], '2026-10-20T10:01:30Z')],
    }))
    expect(graph.rows.map((row) => row.key)).toEqual(['head', 'own2', 'up1', 'own1', 'base:base0'])
    const upstream = graph.rows[2]!
    expect(upstream.kind === 'commit' && [upstream.own, upstream.lane, upstream.tip]).toEqual([false, 1, 'main'])
    expect(graph.edges).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 0, to: 2, toLane: 1, style: 'dashed', own: false, bend: 'early' }),
      expect.objectContaining({ from: 2, fromLane: 1, to: 4, toLane: 1, own: false }),
    ]))
  })

  it('counts the conflicting paths an upstream commit touches', () => {
    const graph = layoutGraph(log({
      behind: 2, base: 'up2',
      upstream: [commit('up2', ['up1'], '2026-10-20T10:04:00Z', { paths: ['a.go', 'b.go'] }), commit('up1', ['base0'], '2026-10-20T10:03:00Z')],
      conflicts: [{ path: 'a.go' }, { path: 'b.go' }],
    }))
    const marked = graph.rows.filter((row) => row.kind === 'commit' && row.conflicts > 0)
    expect(marked.map((row) => row.kind === 'commit' && [row.key, row.conflicts])).toEqual([['up2', 2]])
  })

  it('orders the lanes by instant, whatever offset each author wrote', () => {
    const graph = layoutGraph(log({
      commits: [commit('own2', ['own1'], '2026-10-20T10:00:00+09:00'), commit('own1', ['base0'], '2026-10-20T09:00:00+09:00')],
      behind: 1, base: 'up1', upstream: [commit('up1', ['base0'], '2026-10-20T03:00:00+00:00')],
    }))
    expect(graph.rows.map((row) => row.key).slice(1, 4)).toEqual(['up1', 'own2', 'own1'])
  })

  it('says a truncated log is truncated and leaves its edges dotted', () => {
    const graph = layoutGraph(log({ truncated: true, commits: [commit('own2', ['own1'], '2026-10-20T10:02:00Z')] }))
    expect(graph.rows.map((row) => row.kind)).toEqual(['head', 'commit', 'truncated', 'base'])
    expect(graph.edges).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 1, to: 2, fromLane: 0, toLane: 0, style: 'dotted' }),
      expect.objectContaining({ from: 2, to: 3, fromLane: 0, toLane: 1, style: 'dotted' }),
    ]))
  })

  it('folds more than eight commits into one row and unfolds them on request', () => {
    const commits = Array.from({ length: 10 }, (_, index) => {
      const n = 10 - index
      return commit(`own${n}`, [n === 1 ? 'base0' : `own${n - 1}`], `2026-10-20T10:${String(n).padStart(2, '0')}:00Z`)
    })
    const folded = layoutGraph(log({ head: 'own10', ahead: 10, commits }))
    expect(folded.rows.map((row) => row.kind)).toEqual(['head', ...Array(6).fill('commit'), 'fold', 'base'])
    const fold = folded.rows[7]!
    expect(fold.kind === 'fold' && fold.hidden).toBe(4)
    expect(folded.edges).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 6, to: 7, style: 'dotted' }),
      expect.objectContaining({ from: 7, to: 8, toLane: 1, style: 'dotted' }),
    ]))
    expect(layoutGraph(log({ head: 'own10', ahead: 10, commits }), true).rows).toHaveLength(12)

    const upstream = [commit('up1', ['base0'], '2026-10-20T09:00:00Z', { paths: ['README.md'] })]
    const behind = layoutGraph(log({ head: 'own10', ahead: 10, behind: 1, base: 'up1', commits, upstream, conflicts: [{ path: 'README.md' }] }))
    expect(behind.rows.filter((row) => row.kind === 'commit').map((row) => row.key)).toEqual(['own10', 'own9', 'own8', 'own7', 'own6', 'up1'])
    expect(behind.edges).toEqual(expect.arrayContaining([expect.objectContaining({ from: 0, to: 6, toLane: 1, style: 'dashed' })]))
  })

  it('reuses the rows a refetch left unchanged', () => {
    const first = layoutGraph(log())
    const next = layoutGraph(log({ commits: [commit('own2', ['own1'], '2026-10-20T10:02:00Z', { subject: 'renamed' }), commit('own1', ['base0'], '2026-10-20T10:01:00Z')] }), false, first)
    expect(next.rows[1]).not.toBe(first.rows[1])
    expect(next.rows[2]).toBe(first.rows[2])
    expect(next.rows[3]).toBe(first.rows[3])
  })
})

describe('readiness', () => {
  it('stays unknown without an answer, and never claims ready', () => {
    const ready = readiness(undefined, undefined)
    expect(ready.items.map((entry) => [entry.key, entry.state, entry.text])).toEqual([
      ['checks', 'unknown', 'Not checked yet'],
      ['conflicts', 'unknown', 'Not checked yet'],
      ['review', 'unknown', 'Not checked yet'],
      ['base', 'unknown', 'Not checked yet'],
    ])
    expect(ready.ready).toBe(false)
    expect(readiness(log(), feedback({ mergeable: 'UNKNOWN', merge_state_status: 'UNKNOWN' })).items.map((entry) => entry.state))
      .toEqual(['ok', 'unknown', 'ok', 'unknown'])
  })

  it('is ready only when GitHub grounds every item', () => {
    const ready = readiness(log(), feedback())
    expect(ready.items.map((entry) => `${entry.key}:${entry.state}:${entry.text}`)).toEqual([
      'checks:ok:1 of 1 check passed',
      'conflicts:ok:None with main',
      'review:ok:Approved by bob',
      'base:ok:GitHub needs no update from main',
    ])
    expect(ready.ready).toBe(true)
  })

  it('names each blocker', () => {
    const state = (l: RunGitLogResult, f: RunPRFeedbackResult) => readiness(l, f).items.map((entry) => `${entry.key}:${entry.state}:${entry.text}`)
    expect(state(log(), feedback({ checks: [{ name: 'test', status: 'completed', conclusion: 'failure' }, { name: 'lint', status: 'completed', conclusion: 'success' }] }))[0])
      .toBe('checks:blocked:1 of 2 checks passed · 1 failing')
    expect(state(log(), feedback({ checks: [{ name: 'test', status: 'in_progress' }] }))[0]).toBe('checks:pending:0 of 1 check passed · 1 running')
    expect(state(log(), feedback({ checks: [] }))[0]).toBe('checks:unknown:No check has reported yet')
    expect(state(log(), feedback({ error: 'gh: rate limited' }))[0]).toBe('checks:unknown:Incomplete answer from GitHub')
    expect(state(log({ conflicts: [{ path: 'a.go' }, { path: 'b.go' }] }), feedback({ mergeable: 'CONFLICTING', merge_state_status: 'DIRTY' }))[1])
      .toBe('conflicts:blocked:GitHub reports conflicts with main: 2 files')
    expect(state(log(), feedback({
      reviews: [
        { id: 'r1', author: 'bob', body: '', state: 'APPROVED', submitted_at: '2026-10-20T10:05:00Z' },
        { id: 'r2', author: 'bob', body: 'no', state: 'CHANGES_REQUESTED', submitted_at: '2026-10-20T10:06:00Z' },
      ],
    }))[2]).toBe('review:blocked:Changes requested by bob')
    expect(state(log(), feedback({ reviews: [] }))[2]).toBe('review:pending:No approving review yet')
    expect(state(log({ behind: 3 }), feedback({ merge_state_status: 'BEHIND' }))[3]).toBe('base:pending:Behind main by 3 commits')
    const blocked = readiness(log(), feedback({ merge_state_status: 'BLOCKED' }))
    expect(blocked.items[3]).toMatchObject({ state: 'blocked', text: "GitHub blocks the merge until main's branch rules are met" })
    expect(blocked.ready).toBe(false)
    expect(chooseAction(log(), pr, blocked, everyone, true)).toBe('github')
  })

  it('only predicts from the checkout before there is a pull request', () => {
    const items = (l: RunGitLogResult) => readiness(l, undefined).items.map((entry) => `${entry.key}:${entry.state}:${entry.text}`)
    expect(items(log())).toEqual([
      'checks:unknown:Not checked yet',
      'conflicts:unknown:None against main as last fetched',
      'review:unknown:Not checked yet',
      'base:unknown:Up to date with main as last fetched',
    ])
    expect(items(log({ behind: 3, conflicts: [{ path: 'a.go' }] })).filter((entry) => !entry.startsWith('checks') && !entry.startsWith('review'))).toEqual([
      'conflicts:blocked:1 file conflicts with main as last fetched',
      'base:pending:3 commits behind main as last fetched',
    ])
    expect(readiness(log({ conflicts_error: 'merge-tree: output truncated' }), undefined).items[1])
      .toMatchObject({ key: 'conflicts', state: 'unknown', text: 'merge-tree: output truncated' })
  })

  it('lists a draft first and never calls it ready', () => {
    const ready = readiness(log(), feedback({ pull_request: { ...pr, draft: true } }))
    expect(ready.items.map((entry) => `${entry.key}:${entry.state}`)).toEqual(['draft:pending', 'checks:ok', 'conflicts:ok', 'review:ok', 'base:ok'])
    expect(ready.ready).toBe(false)
  })

  it('keeps a partial answer from turning anything green', () => {
    const items = readiness(log(), feedback({ truncated: true })).items
    expect(items.map((entry) => `${entry.key}:${entry.state}`)).toEqual(['checks:unknown', 'conflicts:ok', 'review:unknown', 'base:ok'])
    expect(readiness(log(), feedback({ error: 'gh: HTTP 502' })).items[2]!.state).toBe('unknown')
  })

  it('takes GitHub at its word for a pull request into another branch, naming no files the run base found', () => {
    const release = feedback({ pull_request: { ...pr, base_branch: 'release' }, mergeable: 'CONFLICTING', merge_state_status: 'DIRTY' })
    const ready = readiness(log({ base_ref: 'origin/main', conflicts: [{ path: 'a.go' }] }), release)
    expect(ready.items[1]).toMatchObject({ state: 'blocked', text: 'GitHub reports conflicts with release', short: 'conflicts' })
    expect(resolvePrompt(log({ base_ref: 'origin/main', conflicts: [{ path: 'a.go' }] }), release.pull_request)).toContain(
      'GitHub reports that pull request #128 conflicts with release.\n\nFetch origin/release, merge it into aether/run-1',
    )
  })

  it('holds the merge while the checkout has commits the pull request does not', () => {
    const moved = log({ head: 'own3', commits: [commit('own3', ['own2'], '2026-10-20T10:03:00Z'), ...log().commits] })
    const ready = readiness(moved, feedback())
    expect(ready.items[3]).toMatchObject({ state: 'pending', text: 'Pull request head is behind the branch: 1 unpushed commit' })
    expect(ready.ready).toBe(false)
    expect(headRow(moved, pr, ready).state).toBe('not pushed')
    expect(chooseAction(moved, pr, ready, everyone, true)).toBe('github')
  })
})

describe('headRow', () => {
  it('names the pending action and its state', () => {
    const row = (l: RunGitLogResult | undefined, p: RunPullRequest | null, f?: RunPRFeedbackResult) => {
      const head = headRow(l, p, readiness(l, f))
      return `${head.label} · ${head.state}`
    }
    expect(row(log(), null)).toBe('Open pull request · 2 ahead')
    expect(row(log({ ahead: 0, commits: [] }), null)).toBe('No commits ahead of main · ')
    expect(row(log({ conflicts: [{ path: 'a.go' }] }), pr, feedback({ mergeable: 'CONFLICTING' }))).toBe('Merge into main · 1 conflict')
    expect(row(log({ behind: 2 }), pr, feedback({ merge_state_status: 'BEHIND' }))).toBe('Merge into main · 2 behind')
    expect(row(log(), pr, feedback())).toBe('Merge into main · ready')
    expect(row(log(), pr, feedback({ checks: [] }))).toBe('Merge into main · no checks yet')
    expect(row(log(), { ...pr, draft: true }, feedback({ pull_request: { ...pr, draft: true } }))).toBe('Merge into main · draft')
    expect(row(log(), { ...pr, state: 'merged' }, feedback())).toBe('Merged into main · merged')
  })
})

describe('chooseAction', () => {
  const ready = (l: RunGitLogResult, f?: RunPRFeedbackResult) => readiness(l, f)

  it('chooses one action for each state', () => {
    expect(chooseAction(log(), null, ready(log()), everyone, true)).toBe('open-pr')
    expect(chooseAction(log({ ahead: 0, commits: [] }), null, ready(log()), everyone, false)).toBe('review')
    expect(chooseAction(log({ ahead: 0, commits: [] }), null, ready(log()), everyone, true)).toBeNull()
    const conflicting = feedback({ mergeable: 'CONFLICTING', merge_state_status: 'DIRTY' })
    expect(chooseAction(log(), pr, ready(log(), conflicting), everyone, true)).toBe('resolve')
    expect(chooseAction(log(), pr, ready(log(), feedback()), everyone, true)).toBe('merge')
    expect(chooseAction(log({ behind: 2 }), pr, ready(log({ behind: 2 }), feedback({ merge_state_status: 'BEHIND' })), everyone, true)).toBe('merge')
    const failing = feedback({ checks: [{ name: 'test', status: 'completed', conclusion: 'failure' }] })
    expect(chooseAction(log(), pr, ready(log(), failing), everyone, true)).toBe('github')
    expect(chooseAction(log(), { ...pr, draft: true }, ready(log(), feedback()), everyone, true)).toBe('github')
    expect(chooseAction(log(), { ...pr, state: 'merged' }, ready(log(), feedback()), everyone, true)).toBe('github')
  })

  it('offers nothing the viewer may not do', () => {
    const viewer: Permissions = { publish: false, create: true, merge: true, prompt: false }
    expect(chooseAction(log(), null, ready(log()), viewer, true)).toBeNull()
    expect(chooseAction(log(), null, ready(log()), viewer, false)).toBe('review')
    expect(chooseAction(log(), null, ready(log()), { ...everyone, create: false }, true)).toBeNull()
    expect(chooseAction(log(), pr, ready(log(), feedback()), viewer, true)).toBe('github')
    expect(chooseAction(log(), pr, ready(log(), feedback()), { ...everyone, merge: false }, true)).toBe('github')
    const conflicting = feedback({ mergeable: 'CONFLICTING' })
    expect(chooseAction(log(), pr, ready(log(), conflicting), { ...everyone, prompt: false }, true)).toBe('github')
  })
})

it('names the base and every conflicting file the checkout found in the prompt', () => {
  const text = resolvePrompt(log({ base: 'abcdef0123456789', conflicts: [{ path: 'a.go' }, { path: 'web/b.ts' }] }), null)
  expect(text).toContain('Merging main (abcdef012345) into aether/run-1 conflicts in these files:\n- a.go\n- web/b.ts')
  expect(text).toContain('Fetch main, merge it into aether/run-1')
})
