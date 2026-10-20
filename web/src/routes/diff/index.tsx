import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { Virtualizer, type VirtualizerHandle } from 'virtua'
import { MissingRun } from '@/components/missing-run'
import { Callout } from '@/components/ui/callout'
import { EmptyState } from '@/components/ui/empty-state'
import { GitFlow } from '@/components/workflow/git-flow'
import { api } from '@/lib/api'
import { clockTime } from '@/lib/format'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { CommentCard } from '@/routes/diff/comment-card'
import { FileList } from '@/routes/diff/file-list'
import { Land } from '@/routes/diff/land'
import { parsePatch, type PatchFile } from '@/routes/diff/parse'
import { FilePatch, largeFile, largeText } from '@/routes/diff/patch-view'
import { useReview } from '@/routes/diff/review'
import { ReviewBar } from '@/routes/diff/review-bar'
import { SummaryStrip } from '@/routes/diff/strip'
import { hasFiles, openInFiles } from '@/routes/files/open'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { isRunRoute } from '@/routes/run/views'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { comparisonFrom, comparisonScope, initialDiff, readCheckpoints, type Comparison } from '@/store/diff'

/** Past this many content lines or files only the files near the screen are mounted. */
const largePatch = 1500
const largeFileCount = 100
/** A stored patch longer than this is not re-fetched on a new snapshot: the reader chooses when. */
const largeStored = 1024 * 1024

function collapsedByDefault(file: PatchFile): boolean {
  return file.contentLines > largeFile || file.size > largeText || file.status === 'binary' || file.status === 'deleted'
}

function useParsed(patch: string): PatchFile[] {
  const previous = useRef<PatchFile[]>([])
  return useMemo(() => (previous.current = parsePatch(patch, previous.current)), [patch])
}

/** The run's changes against the comparison chosen in the strip: its effective
 * base, its starting point, or a recorded checkpoint. */
export function ChangesView({ runID, agent, agentName }: { runID: string; agent: AgentTerminal; agentName: string }) {
  const caps = useCapability()
  const run = useStore((s) => s.runs[runID])
  const route = useStore((s) => s.route)
  const state = useStore((s) => s.diffs[runID] ?? initialDiff)
  const [comparison, setComparison] = useState<Comparison>({ kind: 'all' })
  const [asked, setAsked] = useState<string | null>(null)
  const wrapping = useStore((s) => s.diffWrap)
  const setWrapping = useStore((s) => s.setDiffWrap)
  const coarse = useMediaQuery(coarsePointer)
  const wrap = wrapping ?? coarse
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const [current, setCurrent] = useState<string | null>(null)
  const ids = useId()
  const [focusedFile, setFocusedFile] = useState<number | null>(null)
  const list = useRef<VirtualizerHandle>(null)
  const scroller = useRef<HTMLDivElement>(null)
  const from = comparisonFrom(comparison, state.forkTree)
  const behind = usePatch(run ? runID : '', from)
  // The stored patch answers for one comparison; another one's is not shown as this one's.
  const answered = state.from === from
  const files = useParsed(answered ? state.patch : '')
  const virtual = useMemo(
    () => files.length > largeFileCount || files.reduce((total, file) => total + file.contentLines, 0) > largePatch,
    [files],
  )
  const failed = answered && state.status === 'error'
  const loading = !answered || state.status === 'loading'
  const truncated = answered && state.truncated
  const historyNotice = comparison.kind === 'all' ? state.snapshots.find((entry) => entry.historyGap || entry.snapshotError) : undefined
  const scope = comparisonScope(comparison)
  const against = `${run?.base_branch || 'the base branch'}${state.base ? ` @ ${state.base.slice(0, 7)}` : ''}`
  const review = useMemo(() => ({ runID, scope }), [runID, scope])

  const toggle = useCallback((path: string, next: boolean) => setCollapsed((all) => ({ ...all, [path]: next })), [])
  const open = useCallback((path: string) => {
    openInFiles(runID, { path })
  }, [runID])
  const jump = useCallback((path: string) => {
    setCurrent(path)
    setCollapsed((all) => ({ ...all, [path]: false }))
    const index = files.findIndex((file) => file.path === path)
    requestAnimationFrame(() => {
      if (virtual) list.current?.scrollToIndex(index, { align: 'start' })
      else document.getElementById(`${ids}-${index}`)?.scrollIntoView({ block: 'start' })
    })
  }, [files, virtual, ids])

  const workspaceID = run?.workspace_id
  const live = useStore((s) => s.connection === 'live')
  const historyRead = state.history !== undefined || state.historyError !== undefined
  useEffect(() => {
    if (workspaceID && live && !historyRead) void readCheckpoints(useStore, api, { id: runID, workspace_id: workspaceID })
  }, [runID, workspaceID, live, historyRead])

  // A link to the current diff, or to one file in it, says so in the route;
  // the file is opened once the diff has it.
  useEffect(() => {
    if (!isRunRoute(route, runID) || route.params.view !== 'changes' || (!route.params.file && route.params.diff !== 'current')) return
    setComparison({ kind: 'all' })
    if (route.params.file) setAsked(route.params.file)
  }, [route, runID])
  useEffect(() => {
    if (!asked || comparison.kind !== 'all') return
    if (files.some((file) => file.path === asked)) jump(asked)
    else if (loading) return
    setAsked(null)
  }, [asked, comparison, files, loading, jump])

  if (!run) return <MissingRun />

  const canOpen = hasFiles(run, caps)
  const openable = (file: PatchFile) => file.status !== 'deleted' && file.status !== 'binary'
  const patchAt = (file: PatchFile, index: number) => (
    <FilePatch
      key={file.path}
      id={`${ids}-${index}`}
      file={file}
      wrap={wrap}
      lineNumbers
      collapsed={collapsed[file.path] ?? collapsedByDefault(file)}
      onCollapsedChange={toggle}
      onOpen={canOpen && openable(file) ? open : undefined}
      review={review}
    />
  )
  return (
    <div className="@container flex h-full min-h-0 min-w-0 flex-col bg-canvas">
      <Land run={run} />
      <div className="max-h-[45%] shrink-0 overflow-y-auto p-2 empty:hidden">
        <GitFlow runId={run.id} />
      </div>
      <SummaryStrip
        run={run}
        files={files}
        comparison={comparison}
        onCompare={setComparison}
        wrap={wrap}
        onWrap={setWrapping}
        loading={state.status === 'loading'}
        behind={behind}
        onRefresh={() => {
          useStore.getState().refreshDiff(runID)
          loadPatch(runID, from)
        }}
        onJump={jump}
        onCollapseAll={(next) => setCollapsed(Object.fromEntries(files.map((file) => [file.path, next])))}
        publishable={!state.recorded && (comparison.kind !== 'all' || files.length > 0)}
      />
      <ReviewBar run={run} agent={agent} agentName={agentName} files={files} scope={scope} />
      {(files.length > 0 || (!loading && !failed)) && <Unplaced runID={runID} files={files} scope={scope} />}
      {answered && state.recorded && (
        <Callout tone="neutral" role="status" className="m-2 shrink-0">
          {comparison.kind === 'all'
            ? 'Checkout removed. Showing the last recorded changes.'
            : 'Checkout removed. Showing changes up to the last recorded snapshot.'}
        </Callout>
      )}
      {(historyNotice?.historyGap || historyNotice?.snapshotError) && (
        <Callout tone={historyNotice.snapshotError ? 'failed' : 'needs-you'} role="status" className="m-2 shrink-0">
          {historyNotice.snapshotError && <p>{historyNotice.snapshotError}</p>}
          <p>Some snapshot history is unavailable. All changes is separate and still shows the run's changes against its base.</p>
        </Callout>
      )}
      {failed && (
        <Callout tone="failed" role="alert" className="m-2 shrink-0">
          {state.error ?? 'The diff could not be loaded.'}
          {comparison.kind !== 'all' && <p>Choose All changes to view the run's changes against its base.</p>}
        </Callout>
      )}
      {truncated && (
        <Callout tone="needs-you" className="m-2 shrink-0">
          The server returned an incomplete diff. Refresh it or fetch the run branch to read the complete change.
        </Callout>
      )}
      <div className="flex min-h-0 flex-1">
        {files.length > 0 && <FileList files={files} current={current} onJump={jump} />}
        <div ref={scroller} className="min-w-0 flex-1 overflow-x-hidden overflow-y-auto">
          {virtual ? (
            <div onFocus={(event) => {
              const id = (event.target as HTMLElement).closest('section')?.id ?? ''
              if (id.startsWith(`${ids}-`)) setFocusedFile(Number(id.slice(ids.length + 1)))
            }}>
              <Virtualizer ref={list} scrollRef={scroller} data={files} keepMounted={focusedFile !== null && focusedFile < files.length ? [focusedFile] : undefined}>
                {patchAt}
              </Virtualizer>
            </div>
          ) : (
            files.map(patchAt)
          )}
          {files.length === 0 && !failed && <Empty comparison={comparison} loading={loading} recorded={state.recorded} against={against} />}
        </div>
      </div>
    </div>
  )
}

/** Comments on a file this diff no longer has. They stay, with their quote, until sent or deleted. */
function Unplaced({ runID, files, scope }: { runID: string; files: PatchFile[]; scope: string }) {
  const { comments } = useReview(runID)
  const gone = comments.filter((comment) => comment.scope === scope && !files.some((file) => file.path === comment.path))
  if (gone.length === 0) return null
  return (
    <section aria-label="Outdated comments" className="flex max-h-[40%] shrink-0 flex-col gap-2 overflow-y-auto border-b border-seam bg-chrome p-2">
      {gone.map((comment) => (
        <CommentCard key={comment.id} runID={runID} comment={comment} lines={comment.lines} outdated />
      ))}
    </section>
  )
}

function Empty({ comparison, loading, recorded, against }: { comparison: Comparison; loading: boolean; recorded: boolean; against: string }) {
  if (loading) return <p className="p-4 text-ui text-muted">Loading the diff…</p>
  if (comparison.kind === 'since') {
    return <EmptyState title="No changes.">Nothing changed since {clockTime(comparison.checkpoint.time)}.</EmptyState>
  }
  if (comparison.kind === 'start') return <EmptyState title="No changes.">Nothing changed since the run started.</EmptyState>
  if (recorded) return <EmptyState title="No recorded changes.">The last snapshot matched {against}.</EmptyState>
  return <EmptyState title="No changes yet.">Nothing differs from {against}. Files the agent changes show up here.</EmptyState>
}

/** Runs with a fetch in flight, across remounts of the view. A snapshot
 * landing mid-request neither cancels it nor starts a second one: the response
 * records its revision and the next render asks again if needed. */
const fetching = new Set<string>()

function loadPatch(runID: string, from: string): void {
  if (fetching.has(runID)) return
  const store = useStore.getState()
  const at = store.diffs[runID]?.revision ?? 0
  fetching.add(runID)
  store.setDiff(runID, { status: 'loading' })
  const request = from ? api.runPatch(runID, { from }) : api.runPatch(runID)
  request
    .then((patch) => {
      fetching.delete(runID)
      useStore.getState().applyPatch(patch, at, from)
    })
    .catch((err: unknown) => {
      fetching.delete(runID)
      // Recorded as answered so a failure cannot spin. The patch already on
      // screen stays, unless it belongs to another comparison.
      const kept = useStore.getState().diffs[runID]?.from === from
      useStore.getState().setDiff(runID, {
        status: 'error',
        fetched: at,
        error: err instanceof Error ? err.message : String(err),
        ...(!kept && { patch: '', from }),
      })
    })
}

/** Keeps the patch for `from` current, except a large one, which waits for
 * Refresh. Returns whether newer changes than the stored patch exist and are waiting. */
function usePatch(runID: string, from: string): boolean {
  const revision = useStore((s) => s.diffs[runID]?.revision ?? 0)
  const fetched = useStore((s) => s.diffs[runID]?.fetched ?? -1)
  const stored = useStore((s) => s.diffs[runID]?.from ?? '')
  const large = useStore((s) => (s.diffs[runID]?.patch.length ?? 0) > largeStored)
  const same = stored === from

  useEffect(() => {
    if (runID && (!same || (revision !== fetched && !large))) loadPatch(runID, from)
  }, [runID, from, stored, same, revision, fetched, large])

  return same && large && revision !== fetched
}
