import type { Tone } from '@/components/ui/status-dot'
import type { GitLogCommit, RunGitLogResult, RunPRCheck, RunPRFeedbackResult, RunPRReview, RunPullRequest } from '@/lib/types'
import { checkState, countChecks } from '@/routes/diff/publish-pr'

export const rowHeight = 28
export const foldAt = 8
const upstreamKept = 3

/** Lane 0 is the run branch, lane 1 its base. */
export type Lane = 0 | 1

export type GraphRow =
  | { kind: 'head'; key: 'head'; lane: 0 }
  | { kind: 'commit'; key: string; lane: Lane; commit: GitLogCommit; own: boolean; tip?: string; conflicts: number }
  | { kind: 'fold'; key: 'fold'; hidden: number }
  | { kind: 'truncated'; key: 'truncated' }
  | { kind: 'base'; key: string; sha: string; ref: string }

export interface GraphEdge {
  key: string
  from: number
  fromLane: Lane
  to: number
  toLane: Lane
  /** A merge leaves its row at once; a branch point bends into its parent at the end. */
  bend: 'early' | 'late'
  own: boolean
  style: 'solid' | 'dashed' | 'dotted'
}

export interface GitGraph {
  rows: GraphRow[]
  edges: GraphEdge[]
  lanes: 1 | 2
}

/** Two newest-first lists interleaved by time, each keeping its own order. Times carry their author's offset. */
function interleave(a: GitLogCommit[], b: GitLogCommit[]): { commit: GitLogCommit; own: boolean }[] {
  const out: { commit: GitLogCommit; own: boolean }[] = []
  let i = 0
  let j = 0
  while (i < a.length || j < b.length) {
    const takeOwn = j >= b.length || (i < a.length && Date.parse(a[i]!.authored_at) >= Date.parse(b[j]!.authored_at))
    out.push(takeOwn ? { commit: a[i++]!, own: true } : { commit: b[j++]!, own: false })
  }
  return out
}

function sameRow(a: GraphRow, b: GraphRow): boolean {
  if (a.kind !== 'commit' || b.kind !== 'commit') return JSON.stringify(a) === JSON.stringify(b)
  return a.lane === b.lane && a.own === b.own && a.tip === b.tip && a.conflicts === b.conflicts &&
    a.commit.subject === b.commit.subject && a.commit.author === b.commit.author && a.commit.authored_at === b.commit.authored_at
}

/**
 * Hidden commits stand on the fold row, so edges through the fold stay connected. A row equal to
 * `previous`'s row of the same key is that same object, so memoised rows skip the re-render.
 */
export function layoutGraph(log: RunGitLogResult, expanded = false, previous?: GitGraph): GitGraph {
  const own = log.commits ?? []
  const upstream = log.upstream ?? []
  const conflicting = new Set((log.conflicts ?? []).map((conflict) => conflict.path))
  const all = interleave(own, upstream)
  const folding = !expanded && all.length > foldAt
  // A fold keeps the newest upstream commits in sight: they are what a merge has to take in.
  const keep = new Set(folding ? [...upstream.slice(0, upstreamKept), ...own.slice(0, foldAt - 2 - Math.min(upstream.length, upstreamKept))] : [])
  const shown = folding ? all.filter((entry) => keep.has(entry.commit)) : all
  const rows: GraphRow[] = [{ kind: 'head', key: 'head', lane: 0 }]
  const place = new Map<string, { row: number; lane: Lane }>()
  for (const { commit, own: mine } of shown) {
    place.set(commit.sha, { row: rows.length, lane: mine ? 0 : 1 })
    rows.push({
      kind: 'commit',
      key: commit.sha,
      lane: mine ? 0 : 1,
      commit,
      own: mine,
      tip: commit.sha === log.head ? log.branch : commit.sha === log.base && !mine ? log.base_ref : undefined,
      conflicts: mine ? 0 : (commit.paths ?? []).filter((path) => conflicting.has(path)).length,
    })
  }
  if (folding) {
    const fold = rows.length
    for (const { commit, own: mine } of all.filter((entry) => !keep.has(entry.commit))) place.set(commit.sha, { row: fold, lane: mine ? 0 : 1 })
    rows.push({ kind: 'fold', key: 'fold', hidden: all.length - shown.length })
  }
  const truncated = log.truncated ? rows.length : -1
  if (log.truncated) rows.push({ kind: 'truncated', key: 'truncated' })
  const base = log.merge_base ? rows.length : -1
  if (log.merge_base) {
    place.set(log.merge_base, { row: base, lane: 1 })
    rows.push({ kind: 'base', key: `base:${log.merge_base}`, sha: log.merge_base, ref: log.base_ref })
  }

  const edges = new Map<string, GraphEdge>()
  const add = (edge: Omit<GraphEdge, 'key'>) => {
    if (edge.from === edge.to) return
    const key = `${edge.from}:${edge.fromLane}>${edge.to}:${edge.toLane}`
    if (!edges.has(key)) edges.set(key, { ...edge, key })
  }
  const fold = rows.findIndex((row) => row.kind === 'fold')
  for (const { commit, own: mine } of all) {
    const at = place.get(commit.sha)!
    commit.parents.forEach((parent, index) => {
      // An unknown parent is past the truncation, or else at or below the merge base.
      const target = place.get(parent) ?? (truncated >= 0 ? { row: truncated, lane: at.lane } : base >= 0 ? { row: base, lane: 1 as Lane } : undefined)
      if (!target) return
      const elided = target.row === truncated || at.row === fold || target.row === fold
      add({ from: at.row, fromLane: at.lane, to: target.row, toLane: target.lane, own: mine, bend: index > 0 ? 'early' : 'late', style: elided ? 'dotted' : 'solid' })
    })
  }
  if (truncated >= 0 && base >= 0) {
    for (const lane of [0, 1] as Lane[]) {
      if ([...edges.values()].some((edge) => edge.to === truncated && edge.toLane === lane)) {
        add({ from: truncated, fromLane: lane, to: base, toLane: 1, own: lane === 0, bend: 'late', style: 'dotted' })
      }
    }
  }
  const ownTip = place.get(log.head) ?? (own.length === 0 && base >= 0 ? { row: base, lane: 1 as Lane } : undefined)
  if (ownTip) add({ from: 0, fromLane: 0, to: ownTip.row, toLane: ownTip.lane, own: true, bend: 'late', style: 'dashed' })
  const baseTip = upstream.length > 0 ? place.get(log.base) : undefined
  if (baseTip) add({ from: 0, fromLane: 0, to: baseTip.row, toLane: baseTip.lane, own: false, bend: 'early', style: 'dashed' })

  const before = new Map(previous?.rows.map((row) => [row.key, row]))
  rows.forEach((row, index) => {
    const old = before.get(row.key)
    if (old && sameRow(old, row)) rows[index] = old
  })
  const lanes = rows.some((row) => (row.kind === 'commit' && row.lane === 1) || row.kind === 'base') ? 2 : 1
  return { rows, edges: [...edges.values()], lanes }
}

export type ItemState = 'ok' | 'pending' | 'blocked' | 'unknown'

export interface ReadinessItem {
  key: 'checks' | 'conflicts' | 'review' | 'base'
  label: string
  state: ItemState
  text: string
  /** The words the head row shows when this item is what holds the merge. */
  short: string
  tone: Tone
}

export interface Readiness {
  items: ReadinessItem[]
  ready: boolean
}

export const notChecked = 'Not checked yet'

const labels: Record<ReadinessItem['key'], string> = { checks: 'Checks', conflicts: 'Conflicts', review: 'Review', base: 'Base' }
const tones: Record<ItemState, Tone> = { ok: 'done', pending: 'needs-you', blocked: 'failed', unknown: 'neutral' }

function item(key: ReadinessItem['key'], state: ItemState, text = notChecked, short = text, tone = tones[state]): ReadinessItem {
  return { key, label: labels[key], state, text, short, tone }
}

function plural(count: number, one: string, many = `${one}s`): string {
  return `${count} ${count === 1 ? one : many}`
}

/** A partial answer may be missing entries: it can report a failure it saw, never that all passed. */
function checksItem(feedback: RunPRFeedbackResult | undefined): ReadinessItem {
  if (!feedback?.pull_request) return item('checks', 'unknown')
  const checks = feedback.checks ?? []
  const counts = countChecks(checks)
  if (counts.failing) return item('checks', 'blocked', checkCounts(checks), `${counts.failing} failing`)
  if (feedback.error || feedback.truncated) return item('checks', 'unknown', 'Incomplete answer from GitHub', 'checks incomplete')
  if (counts.running) return item('checks', 'pending', checkCounts(checks), 'checks running', 'working')
  if (!checks.length) return item('checks', 'unknown', 'No check has reported yet', 'no checks yet')
  return item('checks', 'ok', checkCounts(checks), 'checks passed')
}

/** Each reviewer's latest approval or change request; comments alone decide nothing. */
export function reviewVerdicts(reviews: RunPRReview[]): { approved: string[]; changes: string[] } {
  const latest = new Map<string, RunPRReview>()
  for (const review of [...reviews].sort((a, b) => a.submitted_at.localeCompare(b.submitted_at))) {
    if (review.state === 'APPROVED' || review.state === 'CHANGES_REQUESTED' || review.state === 'DISMISSED') latest.set(review.author, review)
  }
  const approved: string[] = []
  const changes: string[] = []
  for (const [author, review] of latest) {
    if (review.state === 'APPROVED') approved.push(author)
    if (review.state === 'CHANGES_REQUESTED') changes.push(author)
  }
  return { approved, changes }
}

function reviewItem(feedback: RunPRFeedbackResult | undefined): ReadinessItem {
  if (!feedback?.pull_request) return item('review', 'unknown')
  const { approved, changes } = reviewVerdicts(feedback.reviews ?? [])
  if (changes.length) return item('review', 'blocked', `Changes requested by ${changes.join(', ')}`, 'changes requested')
  if (feedback.error || feedback.truncated) return item('review', 'unknown', 'Incomplete answer from GitHub', 'reviews incomplete')
  if (approved.length) return item('review', 'ok', `Approved by ${approved.join(', ')}`, 'approved')
  return item('review', 'pending', 'No approving review yet', 'awaiting review')
}

/** The log compares with origin/<run base>; a pull request into another branch is not what it measured. */
export function comparedWith(log: RunGitLogResult | undefined, pr: RunPullRequest | null | undefined): RunGitLogResult | undefined {
  if (!log || log.error) return undefined
  if (pr && log.base_ref !== pr.base_branch && log.base_ref !== `origin/${pr.base_branch}`) return undefined
  if (pr?.state === 'open' && log.head !== pr.head_oid) return undefined
  return log
}

/** Why the log says nothing about the pull request, when it does not. */
function apart(log: RunGitLogResult | undefined, pr: RunPullRequest | null | undefined): { text: string; short: string } | undefined {
  if (!log || log.error || !pr || comparedWith(log, pr)) return undefined
  if (log.head !== pr.head_oid) return { text: `Checkout at ${log.head.slice(0, 7)} is not the pull request's head ${pr.head_oid.slice(0, 7)}`, short: 'not pushed' }
  return { text: `Compared with ${log.base_ref}, not ${pr.base_branch}`, short: notChecked }
}

export function readiness(log: RunGitLogResult | undefined, feedback: RunPRFeedbackResult | undefined): Readiness {
  const pr = feedback?.pull_request
  const read = comparedWith(log, pr)
  const elsewhere = apart(log, pr)
  const base = read?.base_ref || 'the base'
  const conflicts = read?.conflicts
  const items = [
    checksItem(feedback),
    !conflicts
      ? item('conflicts', 'unknown', elsewhere?.text, elsewhere?.short)
      : conflicts.length
        ? item('conflicts', 'blocked', `${plural(conflicts.length, 'file')} ${conflicts.length === 1 ? 'conflicts' : 'conflict'} with ${base}`, plural(conflicts.length, 'conflict'), 'needs-you')
        : item('conflicts', 'ok', `None with ${base}`),
    reviewItem(feedback),
    !read
      ? item('base', 'unknown', elsewhere?.text, elsewhere?.short)
      : read.behind > 0
        ? item('base', 'pending', `${plural(read.behind, 'commit')} behind ${base}`, `${read.behind} behind`)
        : item('base', 'ok', `Up to date with ${base}`),
  ]
  return { items, ready: items.every((entry) => entry.state === 'ok') }
}

export type PRWord = 'Draft' | 'Open' | 'Merged' | 'Closed'

export function prWord(pr: RunPullRequest): PRWord {
  if (pr.state === 'merged') return 'Merged'
  if (pr.state === 'closed') return 'Closed'
  return pr.draft ? 'Draft' : 'Open'
}

export const prTone: Record<PRWord, Tone> = { Draft: 'ended', Open: 'working', Merged: 'done', Closed: 'closed' }

export interface HeadRow {
  label: string
  state: string
  tone: Tone
}

export function headRow(log: RunGitLogResult | undefined, pr: RunPullRequest | null | undefined, ready: Readiness): HeadRow {
  const base = pr?.base_branch || log?.base_ref || 'the base'
  if (pr?.state === 'merged') return { label: `Merged into ${base}`, state: 'merged', tone: 'done' }
  if (pr?.state === 'closed') return { label: 'Closed without merging', state: 'closed', tone: 'closed' }
  const blocker = ready.items.find((entry) => entry.state === 'blocked') ?? ready.items.find((entry) => entry.state !== 'ok')
  const conflicts = ready.items.find((entry) => entry.key === 'conflicts')!
  if (!pr) {
    if (log && !log.error && log.ahead === 0) return { label: `No commits ahead of ${base}`, state: log.behind ? `${log.behind} behind` : '', tone: 'neutral' }
    const state = conflicts.state === 'blocked' ? conflicts : undefined
    return { label: 'Open pull request', state: state?.short ?? (log && !log.error ? `${log.ahead} ahead` : ''), tone: state?.tone ?? 'neutral' }
  }
  if (log && !log.error && log.behind > 0 && conflicts.state === 'ok') return { label: `Update from ${base}`, state: `${log.behind} behind`, tone: 'needs-you' }
  if (ready.ready) return { label: `Merge into ${base}`, state: 'ready', tone: 'done' }
  return { label: `Merge into ${base}`, state: blocker!.short, tone: blocker!.tone }
}

export type ActionKind = 'review' | 'open-pr' | 'resolve' | 'merge' | 'github'

export interface Permissions {
  /** The viewer may push, open and merge through the run's live checkout. */
  publish: boolean
  /** The server answers the Publish dialog's `run.git.status` and `run.pr.create`. */
  create: boolean
  /** The server answers `run.pr.merge`. */
  merge: boolean
  /** The viewer may send the run's agent a message now. */
  prompt: boolean
}

/** The one primary action the card offers; null when the viewer can take none here. */
export function chooseAction(
  log: RunGitLogResult | undefined,
  pr: RunPullRequest | null | undefined,
  ready: Readiness,
  can: Permissions,
  onChanges: boolean,
): ActionKind | null {
  if (pr && pr.state !== 'open') return 'github'
  const conflicted = ready.items.find((entry) => entry.key === 'conflicts')!.state === 'blocked'
  if (conflicted && can.prompt) return 'resolve'
  if (!pr) {
    if (log && !log.error && log.ahead > 0 && can.publish && can.create && !conflicted) return 'open-pr'
    return onChanges ? null : 'review'
  }
  if (!pr.draft && !conflicted && comparedWith(log, pr) && can.publish && can.merge && !ready.items.some((entry) => entry.state === 'blocked')) return 'merge'
  return 'github'
}

export function resolvePrompt(log: RunGitLogResult): string {
  const paths = (log.conflicts ?? []).map((conflict) => `- ${conflict.path}`).join('\n')
  return `Merging ${log.base_ref} (${log.base.slice(0, 12)}) into ${log.branch} conflicts in these files:\n${paths}\n\nMerge ${log.base_ref} into ${log.branch}, resolve each conflict keeping the intent of both sides, run the checks, and commit the merge.`
}

export type PillState = 'failing' | 'running' | 'passing'

export interface CheckPill {
  key: string
  name: string
  state: PillState
  word: string
  check: RunPRCheck
}

const pillOrder: PillState[] = ['failing', 'running', 'passing']
const pillWord: Record<PillState, string> = { failing: 'failed', running: 'running', passing: 'passed' }

export function checkPills(checks: RunPRCheck[]): CheckPill[] {
  return checks
    .map((check, index) => {
      const state = checkState(check)
      return { key: `${check.name}:${check.url ?? index}`, name: check.name, state, word: pillWord[state], check }
    })
    .sort((a, b) => pillOrder.indexOf(a.state) - pillOrder.indexOf(b.state))
}

/** "7 of 9 checks passed · 1 running". */
export function checkCounts(checks: RunPRCheck[]): string {
  if (!checks.length) return 'No checks yet'
  const counts = countChecks(checks)
  return [
    `${counts.passing} of ${checks.length} ${checks.length === 1 ? 'check' : 'checks'} passed`,
    counts.failing && `${counts.failing} failing`,
    counts.running && `${counts.running} running`,
  ].filter(Boolean).join(' · ')
}
