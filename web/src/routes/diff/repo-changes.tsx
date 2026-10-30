import { useEffect, useId, useMemo, useRef, useState, type CSSProperties } from 'react'
import { Virtualizer, type VirtualizerHandle } from 'virtua'
import { GitBranch, RefreshCw } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { EmptyState } from '@/components/ui/empty-state'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { coarsePointer, useElementSize, useMediaQuery } from '@/lib/hooks'
import type { RunRepo } from '@/lib/types'
import { cn } from '@/lib/utils'
import type { PatchFile } from '@/routes/diff/parse'
import { DiffSkeleton } from '@/routes/diff/diff-skeleton'
import { ChangedFileRow, FileList } from '@/routes/diff/file-list'
import { isLargeDiff, useOffsetTop } from '@/routes/diff/large-diff'
import { collapsedByDefault, Counts, FilePatch } from '@/routes/diff/patch-view'
import { PublishDialog } from '@/routes/diff/publish-dialog'
import { RepoPR } from '@/routes/diff/repo-pr'
import { useRepoPatch } from '@/routes/diff/repo-patches'
import { useRepoRuns, type ScopedRecord } from '@/routes/diff/repos'
import { repoReviewKey } from '@/routes/diff/review'
import { ReviewBar } from '@/routes/diff/review-bar'
import { Unplaced } from '@/routes/diff/unplaced'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { isRunRoute } from '@/routes/run/views'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

export const allRepos = 'all'

export function useSelectedRepo(runID: string): string | undefined {
  return useStore((s) => (isRunRoute(s.route, runID) && s.route.params.view === 'changes' ? s.route.params.repo : undefined))
}

function select(runID: string, repo: RunRepo | typeof allRepos) {
  useStore.getState().navigate('run', { runId: runID, view: 'changes', ...(repo === allRepos ? { repo: allRepos } : repo.primary ? {} : { repo: repo.workspace_id }) })
}

function plural(count: number): string {
  return `${count} file${count === 1 ? '' : 's'}`
}

export function RepoTabs({ run, selected }: { run: RunRecord; selected: string }) {
  const repos = useRepoRuns(run)
  const primaryCount = useStore((s) => s.diffs[run.id]?.snapshots[0]?.files.length)
  return (
    <div className="flex h-9 shrink-0 items-center border-b border-seam px-2 coarse:h-12">
      <Tabs value={selected} onValueChange={(value) => select(run.id, value === allRepos ? allRepos : repos.find((entry) => entry.repo.workspace_id === value)!.repo)}>
        <TabsList look="segmented" aria-label="Repos" className="overflow-x-auto [scrollbar-width:none]">
          {repos.map(({ repo, run: scoped }) => (
            <RepoTab key={repo.workspace_id} repo={repo} run={scoped} knownCount={repo.primary ? primaryCount : undefined} />
          ))}
          <TabsTrigger value={allRepos}>All repos</TabsTrigger>
        </TabsList>
      </Tabs>
    </div>
  )
}

function RepoTab({ repo, run, knownCount }: { repo: RunRepo; run: ScopedRecord; knownCount: number | undefined }) {
  const patch = useRepoPatch(run.id, repo.primary ? '' : repo.workspace_id, knownCount === undefined)
  const count = knownCount ?? (patch.loading ? undefined : patch.files.length)
  return (
    <TabsTrigger value={repo.workspace_id}>
      {repo.name}
      {count !== undefined && <span aria-label={plural(count)} className="tabular-nums text-muted">{count}</span>}
    </TabsTrigger>
  )
}

export function AllRepos({ run, agent, agentName }: { run: RunRecord; agent: AgentTerminal; agentName: string }) {
  const repos = useRepoRuns(run)
  return (
    <div className="h-full min-h-0 overflow-y-auto bg-canvas">
      {repos.map(({ repo, run: scoped }) => (
        <RepoSummary key={repo.workspace_id} repo={repo} run={scoped} agent={agent} agentName={agentName} />
      ))}
    </div>
  )
}

const listed = 100

function RepoSummary({ repo, run, agent, agentName }: { repo: RunRepo; run: ScopedRecord; agent: AgentTerminal; agentName: string }) {
  const patch = useRepoPatch(run.id, repo.primary ? '' : repo.workspace_id)
  const open = (path: string) => {
    useStore.getState().navigate('run', { runId: run.id, view: 'changes', file: path, ...(repo.primary ? {} : { repo: repo.workspace_id }) })
  }
  const additions = patch.files.reduce((sum, file) => sum + file.additions, 0)
  const deletions = patch.files.reduce((sum, file) => sum + file.deletions, 0)
  return (
    <section aria-label={repo.name} className="border-b border-seam">
      <div className="flex min-h-9 flex-wrap items-center gap-x-3 gap-y-0.5 bg-chrome px-3 py-1 text-ui-sm">
        <h3 className="font-medium text-text">{repo.name}</h3>
        <span className="min-w-0 truncate font-code text-muted">{repo.branch}</span>
        {!patch.loading && (
          <span className="flex items-center gap-2 tabular-nums text-muted">
            {plural(patch.files.length)}
            <Counts additions={additions} deletions={deletions} />
          </span>
        )}
        <span className="ml-auto"><RepoPR run={run} agent={agent} agentName={agentName} /></span>
      </div>
      {patch.error && <Callout tone="failed" role="alert" className="m-2">{patch.error}</Callout>}
      {patch.loading && !patch.error && <DiffSkeleton className="p-3" />}
      {!patch.loading && !patch.error && patch.files.length === 0 && <p className="p-3 text-ui text-muted">No changes yet.</p>}
      {patch.files.slice(0, listed).map((file) => (
        <ChangedFileRow key={file.path} file={file} onOpen={open} />
      ))}
      {patch.files.length > listed && <p className="px-3 py-1.5 text-ui-sm text-muted">{patch.files.length - listed} more files are not listed.</p>}
    </section>
  )
}

export function RepoChanges({ run, repo, agent, agentName }: { run: ScopedRecord; repo: RunRepo; agent: AgentTerminal; agentName: string }) {
  const caps = useCapability()
  const route = useStore((s) => s.route)
  const data = useRepoPatch(run.id, repo.workspace_id)
  const { files } = data
  const ids = useId()
  const wrapping = useStore((s) => s.diffWrap)
  const coarse = useMediaQuery(coarsePointer)
  const wrap = wrapping ?? coarse
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const [current, setCurrent] = useState<string | null>(null)
  const key = repoReviewKey(run.id, repo.workspace_id)
  const review = useMemo(() => ({ runID: key, scope: '' }), [key])
  const handled = useRef<string>(undefined)
  const virtual = useMemo(() => isLargeDiff(files), [files])
  const list = useRef<VirtualizerHandle>(null)
  const scroller = useRef<HTMLDivElement>(null)
  const [content, setContent] = useState<HTMLDivElement | null>(null)
  const [start, setStart] = useState<HTMLDivElement | null>(null)
  const startMargin = useOffsetTop(content, start)
  const [setToolbar, toolbar] = useElementSize<HTMLDivElement>()
  const [focusedFile, setFocusedFile] = useState<number | null>(null)

  const jump = (path: string) => {
    setCurrent(path)
    setCollapsed((all) => ({ ...all, [path]: false }))
    const index = files.findIndex((file) => file.path === path)
    requestAnimationFrame(() => {
      if (virtual) list.current?.scrollToIndex(index, { align: 'start', offset: -toolbar.height })
      else document.getElementById(`${ids}-${index}`)?.scrollIntoView({ block: 'start' })
    })
  }
  const asked = isRunRoute(route, run.id) ? route.params.file : undefined
  useEffect(() => {
    if (!asked || handled.current === asked || !files.some((file) => file.path === asked)) return
    handled.current = asked
    jump(asked)
  }, [asked, files])

  const publishable = caps.hasMethod('run.git.status')
  const patchAt = (file: PatchFile, index: number) => (
    <FilePatch
      key={file.path}
      id={`${ids}-${index}`}
      file={file}
      wrap={wrap}
      lineNumbers
      collapsed={collapsed[file.path] ?? collapsedByDefault(file)}
      onCollapsedChange={(path, next) => setCollapsed((all) => ({ ...all, [path]: next }))}
      review={review}
    />
  )
  return (
    <div className="@container flex h-full min-h-0 min-w-0 bg-canvas">
      {files.length > 0 && <FileList files={files} current={current} onJump={jump} />}
      <div
        ref={scroller}
        style={{ '--diff-top': `${toolbar.height}px` } as CSSProperties}
        className="min-w-0 flex-1 overflow-x-hidden overflow-y-auto"
      >
        <div ref={setContent} className="relative">
        <div ref={setToolbar} className="sticky top-0 z-20">
          <div role="group" aria-label={`${repo.name} changes`} className="flex min-h-8 min-w-0 flex-wrap items-center gap-x-3 gap-y-0.5 border-b border-seam bg-chrome px-2 py-0.5 text-ui-sm text-muted">
            <span className="tabular-nums">{plural(files.length)}</span>
            <span className="min-w-0 truncate font-code">{repo.branch}</span>
            <RepoPR run={run} agent={agent} agentName={agentName} />
            <span className="ml-auto flex items-center gap-1">
              <Button variant="ghost" size="icon-sm" label="Refresh" onClick={data.refresh}>
                <RefreshCw className={cn(data.loading && 'animate-spin motion-reduce:animate-none')} />
              </Button>
              {publishable && <PublishDialog key={repo.workspace_id} run={run} repoName={repo.name} quiet />}
            </span>
          </div>
          <ReviewBar run={run} agent={agent} agentName={agentName} files={files} scope="" repo={{ key, name: repo.name }} />
        </div>
        <Unplaced runID={key} files={files} scope="" />
        {data.error && <Callout tone="failed" role="alert" className="m-2">{data.error}</Callout>}
        {data.truncated && (
          <Callout tone="needs-you" className="m-2">The server returned an incomplete diff. Refresh it or fetch the session branch to read the complete change.</Callout>
        )}
        {virtual ? (
          <div ref={setStart} onFocus={(event) => {
            const id = (event.target as HTMLElement).closest('section')?.id ?? ''
            if (id.startsWith(`${ids}-`)) setFocusedFile(Number(id.slice(ids.length + 1)))
          }}>
            <Virtualizer
              ref={list}
              scrollRef={scroller}
              startMargin={startMargin}
              data={files}
              keepMounted={focusedFile !== null && focusedFile < files.length ? [focusedFile] : undefined}
            >
              {patchAt}
            </Virtualizer>
          </div>
        ) : (
          files.map(patchAt)
        )}
        {files.length === 0 && !data.error && (data.loading
          ? <DiffSkeleton />
          : <EmptyState icon={<GitBranch />} title="No changes yet">{`Nothing differs from ${run.base_branch || 'the base branch'} in ${repo.name}. Files the agent changes show up here.`}</EmptyState>)}
        </div>
      </div>
    </div>
  )
}
