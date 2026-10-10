import { create } from 'zustand'
import { useShallow } from 'zustand/react/shallow'
import type { RoomMessage } from '@/lib/types'
import { linePrefix, type PatchFile, type PatchLine } from '@/routes/diff/parse'
import { useStore } from '@/store'
import { pruneRuns } from '@/store/runs'

export interface ReviewComment {
  id: string
  path: string
  /** The diff it was written on: '' for the current one, else an interval key. */
  scope: string
  /** The commented lines as that diff showed them: the anchor, and the quote the agent gets. */
  lines: PatchLine[]
  body: string
  /** The text in an open editor; absent once the comment is pinned. */
  draft?: string
}

export interface Review {
  comments: ReviewComment[]
  sending?: boolean
  /** Why the last send left the comments in place. */
  error?: string
  /** The room message that carried the last batch, as the server answered the send. */
  sent?: { message: RoomMessage; count: number }
}

const noReview: Review = { comments: [] }
const noComments: ReviewComment[] = []

// Not a root-store slice, for the reason composer drafts are not: a write
// there reaches a controlled textarea a frame late.
const useReviews = create<Record<string, Review>>(() => ({}))

export function review(runID: string): Review {
  return useReviews.getState()[runID] ?? noReview
}

export function useReview(runID: string): Review {
  return useReviews((reviews) => reviews[runID] ?? noReview)
}

/** Whether a pinned comment waits to be sent. */
export function useReviewing(runID: string): boolean {
  return useReviews((reviews) => reviews[runID]?.comments.some((comment) => comment.body) ?? false)
}

/** One file's comments, the same array until one of them changes. */
export function useFileComments(runID: string, path: string): ReviewComment[] {
  return useReviews(useShallow((reviews) => reviews[runID]?.comments.filter((comment) => comment.path === path) ?? noComments))
}

export function patchReview(runID: string, patch: Partial<Review>): void {
  const next = { ...review(runID), ...patch }
  if (next.comments.length === 0) next.error = undefined
  const reviews = { ...useReviews.getState(), [runID]: next }
  if (next.comments.length === 0 && !next.sending && !next.error && !next.sent) delete reviews[runID]
  useReviews.setState(reviews, true)
}

function text(comment: ReviewComment): string {
  return (comment.draft ?? comment.body).trim()
}

// The editor a click just opened takes the focus once; one that remounts later does not.
let opened = ''

export function claimFocus(id: string): boolean {
  if (opened !== id) return false
  opened = ''
  return true
}

/** Opens an editor on `lines`. A new comment nobody typed into gives way to it. */
export function addComment(runID: string, anchor: Pick<ReviewComment, 'path' | 'scope' | 'lines'>): void {
  opened = crypto.randomUUID()
  const kept = review(runID).comments.filter((comment) => comment.body || text(comment))
  patchReview(runID, { comments: [...kept, { ...anchor, id: opened, body: '', draft: '' }], sent: undefined })
}

export function editComment(runID: string, comment: ReviewComment): void {
  opened = comment.id
  updateComment(runID, comment.id, { draft: comment.body })
}

/** Re-targets an open editor, which keeps the focus wherever its card lands. */
export function growComment(runID: string, id: string, lines: PatchLine[]): void {
  opened = id
  updateComment(runID, id, { lines })
}

export function updateComment(runID: string, id: string, patch: Partial<ReviewComment>): void {
  patchReview(runID, {
    comments: review(runID).comments.map((comment) => (comment.id === id ? { ...comment, ...patch } : comment)),
  })
}

export function removeComment(runID: string, id: string): void {
  patchReview(runID, { comments: review(runID).comments.filter((comment) => comment.id !== id) })
}

/** Pins what the editor holds; an editor left empty removes the comment. */
export function closeEditor(runID: string, id: string, keep: boolean): void {
  const comment = review(runID).comments.find((entry) => entry.id === id)
  if (!comment) return
  const body = keep ? text(comment) : comment.body
  if (body) updateComment(runID, id, { body, draft: undefined })
  else removeComment(runID, id)
}

/** The comments a send carries: everything with text, an open editor's included. */
export function written(comments: ReviewComment[]): ReviewComment[] {
  return comments.filter(text).map((comment) => ({ ...comment, body: text(comment), draft: undefined }))
}

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useReviews.setState({}, true)
  else if (state.runs !== previous.runs) useReviews.setState(pruneRuns(useReviews.getState(), (runID) => runID in state.runs), true)
})

export function commentable(line: PatchLine | undefined): boolean {
  return line !== undefined && line.kind !== 'hunk' && line.kind !== 'meta'
}

/** `to`, pulled back to the last line a range from `from` can reach without leaving its hunk. */
export function reach(lines: PatchLine[], from: number, to: number): number {
  const step = to < from ? -1 : 1
  let at = from
  while (at !== to && commentable(lines[at + step])) at += step
  return at
}

function sameText(a: PatchLine, b: PatchLine): boolean {
  return a.kind === b.kind && a.text === b.text
}

/**
 * Where a comment's lines start in `lines`, or -1. A block still at its line
 * numbers wins; one that moved is followed only when it is the single match,
 * because a guess would pin the comment to the wrong code.
 */
export function locate(lines: PatchLine[], quote: PatchLine[]): number {
  const first = quote[0]
  if (!first) return -1
  const matches: number[] = []
  for (let at = 0; at + quote.length <= lines.length; at++) {
    if (!quote.every((line, i) => sameText(line, lines[at + i]))) continue
    if (lines[at].old === first.old && lines[at].new === first.new) return at
    matches.push(at)
  }
  return matches.length === 1 ? matches[0] : -1
}

export interface Placed {
  comment: ReviewComment
  start: number
  end: number
}

export interface FileReview {
  placed: Placed[]
  /** Written on this diff, on lines it no longer shows. */
  outdated: ReviewComment[]
}

/** A comment from another diff appears where its lines do, and nowhere otherwise. */
export function placeComments(lines: PatchLine[], comments: ReviewComment[], scope: string): FileReview {
  const out: FileReview = { placed: [], outdated: [] }
  for (const comment of comments) {
    const start = locate(lines, comment.lines)
    if (start >= 0) out.placed.push({ comment, start, end: start + comment.lines.length - 1 })
    else if (comment.scope === scope) out.outdated.push(comment)
  }
  return out
}

/** `12`, `12-14`, or the old numbers of lines that were only removed. */
export function lineLabel(lines: PatchLine[]): string {
  const added = lines.filter((line) => line.new !== undefined)
  const side = added.length > 0 ? added.map((line) => line.new!) : lines.map((line) => line.old!)
  const [start, end] = [side[0], side[side.length - 1]]
  return `${start === end ? start : `${start}-${end}`}${added.length > 0 ? '' : ' (removed)'}`
}

export function quoteLines(lines: PatchLine[]): string[] {
  return lines.map((line) => linePrefix[line.kind] + line.text)
}

/** The lines as a one-hunk patch, for a view that renders patch text. */
export function quotePatch(lines: PatchLine[]): string {
  const old = lines.filter((line) => line.old !== undefined)
  const added = lines.filter((line) => line.new !== undefined)
  return [`@@ -${old[0]?.old ?? 0},${old.length} +${added[0]?.new ?? 0},${added.length} @@`, ...quoteLines(lines)].join('\n')
}

function fenced(lines: string[]): string {
  const longest = Math.max(0, ...[...lines.join('\n').matchAll(/`+/g)].map((run) => run[0].length))
  const fence = '`'.repeat(Math.max(3, longest + 1))
  return [`${fence}diff`, ...lines, fence].join('\n')
}

export interface ReviewEntry {
  comment: ReviewComment
  /** The lines as the diff on screen shows them now, or as written when it does not. */
  lines: PatchLine[]
  outdated: boolean
}

/** Every written comment against the diff on screen, in file order and then line order. */
export function reviewEntries(comments: ReviewComment[], files: PatchFile[], scope: string): ReviewEntry[] {
  const order = new Map(files.map((file, index) => [file.path, index]))
  const entries = written(comments).map((comment): ReviewEntry & { at: number } => {
    const lines = files[order.get(comment.path) ?? -1]?.lines ?? []
    const start = locate(lines, comment.lines)
    return start >= 0
      ? { comment, lines: lines.slice(start, start + comment.lines.length), outdated: false, at: start }
      : { comment, lines: comment.lines, outdated: comment.scope === scope, at: Number.MAX_SAFE_INTEGER }
  })
  const rank = (entry: ReviewEntry) => order.get(entry.comment.path) ?? files.length
  return entries.sort((a, b) => rank(a) - rank(b) || a.comment.path.localeCompare(b.comment.path) || a.at - b.at)
}

/**
 * The one message a review becomes. Each comment names its file and lines,
 * quotes them with their diff markers and then says its piece, so the agent
 * can act on it without the diff.
 */
export function reviewMessage(entries: ReviewEntry[]): string {
  const blocks = entries.map((entry, index) => [
    `${index + 1}. ${entry.comment.path}:${lineLabel(entry.lines)}${entry.outdated ? ' (these lines have changed since the comment was written)' : ''}`,
    fenced(quoteLines(entry.lines)),
    entry.comment.body,
  ].join('\n'))
  const count = entries.length === 1 ? '1 review comment' : `${entries.length} review comments`
  return [`${count} on your changes. Each quotes the diff lines it is about; line numbers are the new file's unless marked removed.`, ...blocks].join('\n\n')
}
