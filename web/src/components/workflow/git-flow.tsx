import { useEffect, useMemo, useRef, useState } from 'react'
import { Confirm } from '@/components/confirm'
import { ChevronDown, ChevronRight, ExternalLink, GitBranch } from '@/components/icons'
import { Button, buttonVariants } from '@/components/ui/button'
import { Menu, MenuContent, MenuRadioGroup, MenuRadioItem, MenuTrigger } from '@/components/ui/menu'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Spinner } from '@/components/ui/spinner'
import { StatusDot, toneClasses } from '@/components/ui/status-dot'
import { CheckPills, ReadinessList, Reviews } from '@/components/workflow/git-checks'
import { refreshGitLog, useGitLog, useOfferPrimary } from '@/components/workflow/git-data'
import { GitGraphView } from '@/components/workflow/git-graph'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import type { RoomMessage, RunGitLogResult, RunPRMergeMethod, RunPullRequest } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { receipt, refusal, useAgentSend } from '@/routes/diff/agent-send'
import { openPublish } from '@/routes/diff/publish-dialog'
import { refreshPullRequest, usePullRequest } from '@/routes/diff/publish-pr'
import { useRunAgent } from '@/routes/run/agent-context'
import { implicitControlWindowMs, type AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import {
  checkCounts, chooseAction, headRow, layoutGraph, prTone, prWord, readiness, resolvePrompt,
  type ActionKind, type GitGraph, type HeadRow, type Readiness,
} from '@/store/git-flow'
import { useCapability, useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

type AgentSend = ReturnType<typeof useAgentSend>

const headOnly: GitGraph = { rows: [{ kind: 'head', key: 'head', lane: 0 }], edges: [], lanes: 1 }

const methodLabel: Record<RunPRMergeMethod, string> = { squash: 'Squash and merge', merge: 'Create a merge commit', rebase: 'Rebase and merge' }

/** The run's branch, pull request, checks and merge readiness as one card that updates in place. */
export function GitFlow({ runId, compact = false }: { runId: string; compact?: boolean }) {
  const run = useStore((s) => s.runs[runId])
  const agent = useRunAgent()
  if (!run) return null
  if (!agent) return <Card run={run} compact={compact} send={null} agentName="" />
  return <WithAgent run={run} compact={compact} agent={agent.agent} agentName={agent.agentName} />
}

function WithAgent({ run, compact, agent, agentName }: { run: RunRecord; compact: boolean; agent: AgentTerminal; agentName: string }) {
  const send = useAgentSend(run, agent)
  return <Card run={run} compact={compact} send={send} agentName={agentName} />
}

/** Announces a settled line once it has held for a second, so a burst of refreshes reads once. */
function useAnnounced(text: string): string {
  const [said, setSaid] = useState(text)
  useEffect(() => {
    const timer = window.setTimeout(() => setSaid(text), 1000)
    return () => clearTimeout(timer)
  }, [text])
  return said
}

function Conflicts({ log, head }: { log: RunGitLogResult; head: HeadRow }) {
  return (
    <Popover>
      <PopoverTrigger className={cn(focusRing, 'ml-auto shrink-0 rounded-control pl-2 text-ui-sm tabular-nums underline-offset-2 hover:underline', toneClasses(head.tone).text)}>
        {head.state}
      </PopoverTrigger>
      <PopoverContent align="end" className="w-72 max-w-[90vw] p-2 text-ui-sm">
        <p className="mb-1 text-muted">Conflicts with <span className="font-code">{log.base_ref}</span></p>
        <ul className="grid gap-0.5">
          {(log.conflicts ?? []).map((conflict) => <li key={conflict.path} className="font-code break-all text-text">{conflict.path}</li>)}
        </ul>
      </PopoverContent>
    </Popover>
  )
}

function Card({ run, compact, send, agentName }: { run: RunRecord; compact: boolean; send: AgentSend | null; agentName: string }) {
  const caps = useCapability()
  const self = useSelf()
  const live = run.status === 'running' || run.status === 'needs-attention'
  const tracked = usePullRequest(run, live && caps.hasMethod('run.git.status') && caps.hasMethod('run.pr.feedback'))
  const reads = live && caps.hasMethod('run.git.log')
  const logged = useGitLog(run.id, reads)
  const [expanded, setExpanded] = useState(false)
  const [open, setOpen] = useState(false)
  const [method, setMethod] = useState<RunPRMergeMethod>('squash')
  // What the member is asked to confirm, kept as it was when the dialog opened however the poll moves the pull request.
  const [confirming, setConfirming] = useState<{ pr: RunPullRequest; login?: string; ready: Readiness } | null>(null)
  const [acting, setActing] = useState<ActionKind | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [sent, setSent] = useState<RoomMessage | null>(null)
  const [queued, setQueued] = useState('')
  const delivered = useStore((s) => (sent ? s.roomMessages[run.id]?.find((entry) => entry.id === sent.id) : undefined))

  const log = logged.log
  const feedback = tracked.feedback
  const pr = feedback?.pull_request ?? null
  // A failed refresh keeps the last answer on screen, but not as a fact about now.
  const current = logged.error ? undefined : log
  const judged = useMemo(
    () => (tracked.error && feedback ? { ...feedback, error: tracked.error, mergeable: undefined, merge_state_status: undefined } : feedback),
    [tracked.error, feedback],
  )
  const ready = useMemo(() => readiness(current, judged), [current, judged])
  const laidOut = useRef<GitGraph | undefined>(undefined)
  const full = useMemo(() => (log ? layoutGraph(log, expanded, laidOut.current) : headOnly), [log, expanded])
  useEffect(() => {
    laidOut.current = full
  }, [full])
  const graph = compact && !open ? headOnly : full
  const head = headRow(current, pr, ready)
  const conflicted = ready.items.find((entry) => entry.key === 'conflicts' && entry.state === 'blocked')
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  // The server checks steering as well as push before a merge, as for the agent's own checkout.
  const publish = live && allowed('push', self) && allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const action = chooseAction(current, pr, ready, {
    publish,
    create: caps.hasMethod('run.git.status') && caps.hasMethod('run.pr.create'),
    merge: caps.hasMethod('run.pr.merge'),
    prompt: live && send !== null && send.blocked === null,
  }, !compact)
  useOfferPrimary(run.id, !compact && action !== null)
  const conflictPaths = (current?.conflicts ?? []).map((conflict) => conflict.path).join('\n')
  useEffect(() => setSent(null), [conflictPaths])
  const dropped = useRef<number | undefined>(undefined)
  useEffect(() => () => clearTimeout(dropped.current), [])
  const said = sent ? receipt(delivered ?? sent, 'conflict list', agentName) : null
  const base = log?.base_ref || pr?.base_branch || run.base_branch
  const summary = [
    pr && feedback ? checkCounts(feedback.checks ?? []) : '',
    log ? `${log.ahead} ahead · ${log.behind} behind ${log.base_ref}` : '',
  ].filter(Boolean).join(' · ')
  const status =
    acting === 'merge' ? `Merging #${pr?.number} into ${base}…`
      : acting === 'resolve' ? `Sending the conflict list to ${agentName}…`
        : queued && pr?.state === 'open' ? queued
          : said?.text ?? (logged.reading && !log ? 'Reading the branch…' : summary || (live ? 'Not checked yet' : 'The run is not live; its branch is not read.'))
  const announced = useAnnounced(status)
  const errors = [logged.error, tracked.error, feedback?.error].filter((entry): entry is string => !!entry)

  if (!reads && !log && !pr && !errors.length) return null

  const go = (view: 'changes') => useStore.getState().navigate('run', { runId: run.id, view })
  const resolve = () => {
    if (!send || acting) return
    setActing('resolve')
    setFailure(null)
    setSent(null)
    const settle = (error?: string) => {
      setActing(null)
      if (error) setFailure(error)
    }
    dropped.current = window.setTimeout(() => settle('Control of the run did not arrive, so nothing was sent.'), implicitControlWindowMs + 500)
    send.dispatch(() => {
      clearTimeout(dropped.current)
      void send.post(resolvePrompt(current, pr)).then(
        (posted) => {
          if (posted.state !== 'sent' && posted.state !== 'queued') return settle(refusal(posted))
          setSent(posted)
          settle()
        },
        (cause) => settle(message(cause)),
      )
    })
  }
  const merge = async ({ pr, login }: { pr: RunPullRequest; login?: string }) => {
    setActing('merge')
    setFailure(null)
    setQueued('')
    try {
      const result = await api.runPRMerge({
        run_id: run.id, method, repository: pr.repository, number: pr.number, expected_head: pr.head_oid, expected_login: login, expected_base: pr.base_branch,
      })
      if (result.error) throw new Error(result.error)
      // A merge queue accepts the request and merges later.
      if (!result.merged) setQueued(result.output.stdout?.trim() || `GitHub has not merged #${pr.number}; it is ${result.pull_request?.state ?? pr.state}.`)
    } catch (cause) {
      setFailure(message(cause))
      throw cause
    } finally {
      setActing(null)
      void refreshPullRequest(run)
      refreshGitLog(run.id)
    }
  }

  const spinner = (kind: ActionKind) => acting === kind && <Spinner className="text-current" />
  const primary = (() => {
    switch (action) {
      case 'review':
        return <Button size="sm" onClick={() => go('changes')}>Review changes</Button>
      case 'open-pr':
        return (
          <Button size="sm" onClick={() => { go('changes'); openPublish(run.id) }}>
            Open pull request
          </Button>
        )
      case 'resolve':
        return (
          <Button size="sm" disabled={acting !== null} onClick={resolve}>
            {spinner('resolve')}
            Ask agent to resolve conflicts
          </Button>
        )
      case 'merge':
        return (
          <span className="inline-flex">
            <Button size="sm" className="rounded-r-none" disabled={acting !== null} onClick={() => pr && setConfirming({ pr, login: feedback?.identity, ready })}>
              {spinner('merge')}
              {methodLabel[method]}
            </Button>
            <Menu>
              <MenuTrigger asChild>
                <Button size="sm" className="rounded-l-none border-l border-on-accent/30 px-1.5" disabled={acting !== null} aria-label="Merge method">
                  <ChevronDown aria-hidden />
                </Button>
              </MenuTrigger>
              <MenuContent align="end">
                <MenuRadioGroup value={method} onValueChange={(next) => setMethod(next as RunPRMergeMethod)}>
                  {(Object.keys(methodLabel) as RunPRMergeMethod[]).map((key) => (
                    <MenuRadioItem key={key} value={key}>{methodLabel[key]}</MenuRadioItem>
                  ))}
                </MenuRadioGroup>
              </MenuContent>
            </Menu>
          </span>
        )
      case 'github':
        return pr ? (
          <a className={buttonVariants({ variant: 'secondary', size: 'sm' })} href={pr.url} target="_blank" rel="noreferrer">
            Open on GitHub
            <ExternalLink aria-hidden />
          </a>
        ) : null
      default:
        return null
    }
  })()

  const word = pr ? prWord(pr) : null
  const headContent = (
    <>
      <span className="min-w-0 truncate font-medium text-text">{head.label}</span>
      {current?.conflicts?.length && conflicted && head.state === conflicted.short ? (
        <Conflicts log={current} head={head} />
      ) : (
        head.state && <span className={cn('ml-auto shrink-0 pl-2 text-ui-sm tabular-nums', toneClasses(head.tone).text)}>{head.state}</span>
      )}
    </>
  )
  const commits = (log?.commits?.length ?? 0) + (log?.upstream?.length ?? 0)

  return (
    <section aria-label="Branch and pull request" className="@container/git grid min-w-0 gap-2 rounded-panel border border-seam bg-canvas p-3">
      <header className="flex min-w-0 items-center gap-2 text-ui">
        {pr ? (
          <a className="min-w-0 truncate font-medium text-text underline-offset-2 hover:underline" href={pr.url} target="_blank" rel="noreferrer">
            {pr.title} <span className="font-normal text-muted tabular-nums">#{pr.number}</span>
          </a>
        ) : (
          <span className="flex min-w-0 items-center gap-1.5">
            <GitBranch aria-hidden className="size-3.5 shrink-0 text-muted" />
            <span className="min-w-0 truncate font-code text-ui-sm text-text">{log?.branch || run.branch}</span>
          </span>
        )}
        {word && (
          <span className="ml-auto flex shrink-0 items-center gap-1.5 text-ui-sm text-muted">
            <StatusDot tone={prTone[word]} />
            {word}
          </span>
        )}
      </header>
      <GitGraphView graph={graph} head={headContent} onExpand={() => setExpanded(true)} />
      {compact && log && commits > 0 && (
        <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen(!open)} className="justify-self-start text-muted">
          <ChevronRight aria-hidden className={cn('size-3 transition-transform motion-reduce:transition-none', open && 'rotate-90')} />
          {open ? 'Hide commits' : `Show ${commits} ${commits === 1 ? 'commit' : 'commits'}`}
        </Button>
      )}
      {pr && feedback && (compact ? (
        <p className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
          <StatusDot tone={ready.items[0]!.tone} label={ready.items[0]!.label} />
          <span className="min-w-0 truncate">{checkCounts(feedback.checks ?? [])}</span>
        </p>
      ) : (
        <>
          <CheckPills feedback={feedback} />
          <Reviews feedback={feedback} />
        </>
      ))}
      {!compact && (log || pr) && <ReadinessList ready={ready} />}
      {[...errors, ...(failure ? [failure] : [])].map((error, index) => (
        <p key={index} role="alert" className="min-w-0 break-words whitespace-pre-wrap text-ui-sm text-state-failed">{error}</p>
      ))}
      <footer className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
        <p className={cn('min-w-0 flex-1 basis-40 break-words text-ui-sm', said?.tone === 'failed' ? 'text-state-failed' : 'text-muted')}>{status}</p>
        {primary}
      </footer>
      <p aria-live="polite" className="sr-only">{announced}</p>
      {confirming && (
        <Confirm
          title={`${methodLabel[method]} #${confirming.pr.number}?`}
          description={
            <>
              Merges <span className="font-code">{confirming.pr.head_branch}</span> at{' '}
              <span className="font-code">{confirming.pr.head_oid.slice(0, 7)}</span> into{' '}
              <span className="font-code">{confirming.pr.base_branch}</span> on GitHub through the run's checkout. GitHub refuses it if
              either has changed.
            </>
          }
          action={methodLabel[method]}
          pending="Merging…"
          onConfirm={() => merge(confirming)}
          onDone={() => {}}
          onClose={() => setConfirming(null)}
        >
          <ReadinessList ready={confirming.ready} />
        </Confirm>
      )}
    </section>
  )
}
