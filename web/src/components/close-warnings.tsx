import { useEffect, useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { RunRetention } from '@/components/run-retention'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { GitBranch, GitPullRequest, Hourglass, Lock, MessageSquare, Network, PackageX } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { cn, focusRingShown } from '@/lib/utils'
import { closeChangedText, closeState, readCloseCheck } from '@/lib/close-check'
import { useCommandRunner } from '@/lib/commands'
import { runLabel } from '@/lib/status'
import type { CloseOutcome, CloseWarning, CloseWarningKind, Run, RunCloseCheck } from '@/lib/types'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'

/** Undefined until the server answers. */
export function useCloseCheck(runID: string | null, outcome?: CloseOutcome): RunCloseCheck | undefined {
  const [check, setCheck] = useState<RunCloseCheck>()
  useEffect(() => {
    setCheck(undefined)
    if (!runID) return
    let live = true
    void readCloseCheck(runID, outcome).then((read) => {
      if (live) setCheck(read)
    })
    return () => {
      live = false
    }
  }, [runID, outcome])
  return check
}

const order: CloseWarningKind[] = ['pull_request', 'unmerged_work', 'messages', 'awaited', 'subsessions', 'cleanup_pending', 'protected']

const icons: Record<CloseWarningKind, typeof Lock> = {
  pull_request: GitPullRequest,
  unmerged_work: GitBranch,
  awaited: Hourglass,
  subsessions: Network,
  messages: MessageSquare,
  cleanup_pending: PackageX,
  protected: Lock,
}

/** The height a confirmation without its own scrolling body gives the warnings, so its buttons stay in view. */
export const warningScroll = 'max-h-[min(50dvh,24rem)] overflow-y-auto'

const byRisk = (a: CloseWarning, b: CloseWarning) => order.indexOf(a.kind) - order.indexOf(b.kind)

/** onLeave closes whatever dialog shows the warnings before a session link navigates away; a dialog that does not scroll its body passes a height limit in className. */
export function CloseWarnings({ check, run, onLeave, className }: { check: RunCloseCheck; run: Run; onLeave: () => void; className?: string }) {
  const listed = check.warnings
    .filter((warning) => !warning.subsession && (warning.kind !== 'cleanup_pending' || !retentionShows(run, warning)))
    .sort(byRisk)
  if (!check.error && listed.length === 0 && !run.container_retained_until && !run.cleanup_pending && !run.cleanup_error) return null
  return (
    <div className={cn('flex flex-col gap-2 text-ui-sm', className)}>
      {check.error && <p role="alert" className="break-words text-state-failed">Could not check what closing affects: {check.error.message}</p>}
      {listed.length > 0 && (
        <ul aria-label="What closing affects" className="flex flex-col gap-1.5">
          <Lines warnings={listed} run={run} onLeave={onLeave} all={check.warnings} />
        </ul>
      )}
      <RunRetention run={run} />
    </div>
  )
}

/** Several pull requests of one session read as one line of numbers. */
function Lines({ warnings, run, onLeave, all }: { warnings: CloseWarning[]; run: Run | undefined; onLeave: () => void; all: CloseWarning[] }) {
  const prs = warnings.filter((warning) => warning.kind === 'pull_request' && warning.number && warning.url)
  return warnings.map((warning) => {
    if (prs.includes(warning)) {
      return warning === prs[0] ? <Line key="pull_requests" warning={warning}><PullRequests prs={prs} /></Line> : null
    }
    return (
      <Line key={warning.kind + (warning.url ?? '')} warning={warning}>
        {warning.kind === 'subsessions'
          ? <Subsessions ids={warning.run_ids ?? []} warnings={all} onLeave={onLeave} />
          : <Explain warning={warning} run={run} onLeave={onLeave} />}
      </Line>
    )
  })
}

function PullRequests({ prs }: { prs: CloseWarning[] }) {
  const oldest = prs.map((pr) => pr.observed_at).filter((at) => at !== undefined).sort()[0]
  const checked = oldest ? <>when Aether last checked <RelativeTime at={oldest} /></> : 'when Aether last checked'
  const links = prs.map((pr, i) => (
    <span key={pr.url}>
      {i > 0 && ', '}
      <a href={pr.url} target="_blank" rel="noreferrer" className="whitespace-nowrap text-accent hover:underline">{prRef(pr)}</a>
    </span>
  ))
  return prs.length === 1
    ? <>Pull request {links} was open, not merged, {checked}.</>
    : <>{prs.length} pull requests were open, not merged, {checked}: {links}.</>
}

/** owner/repo#n, read from the pull request's GitHub URL. */
function prRef(pr: CloseWarning): string {
  const [owner, repo] = new URL(pr.url!).pathname.split('/').slice(1, 3)
  return `${owner}/${repo}#${pr.number}`
}

function Line({ warning, children }: { warning: CloseWarning; children: ReactNode }) {
  const Icon = icons[warning.kind]
  return (
    <li className="flex min-w-0 gap-2 break-words">
      <Icon aria-hidden className="mt-0.5 size-3.5 shrink-0 text-icon-faint" />
      <div className="min-w-0">{children}</div>
    </li>
  )
}

function Subsessions({ ids, warnings, onLeave }: { ids: string[]; warnings: CloseWarning[]; onLeave: () => void }) {
  const runs = useStore((s) => s.runs)
  return (
    <>
      Closes with it:
      <ul className="mt-1 flex flex-col gap-1">
        {ids.map((id) => {
          const own = warnings.filter((warning) => warning.subsession === id).sort(byRisk)
          return (
            <li key={id}>
              <span className="font-medium text-text">{runs[id] ? runLabel(runs[id]) : id}</span>
              {own.length > 0 && (
                <ul className="mt-1 flex flex-col gap-1 pl-3">
                  <Lines warnings={own} run={runs[id]} onLeave={onLeave} all={warnings} />
                </ul>
              )}
            </li>
          )
        })}
      </ul>
    </>
  )
}

/** RunRetention already says what a cleanup warning would, unless the check read a state the cached run does not have. */
function retentionShows(run: Run, warning: CloseWarning): boolean {
  return run.cleanup_error ? warning.detail === `Cleanup failed: ${run.cleanup_error}` : Boolean(run.cleanup_pending) && !warning.detail.startsWith('Cleanup failed')
}

const instant = /(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ)/

/** The server writes times as RFC 3339; show them relative, with the exact time on hover. */
function WithTimes({ text }: { text: string }) {
  return text.split(instant).map((part, i) => (i % 2 ? <RelativeTime key={i} at={part} /> : part))
}

function Explain({ warning, run, onLeave }: { warning: CloseWarning; run: Run | undefined; onLeave: () => void }) {
  const self = useSelf()
  switch (warning.kind) {
    case 'pull_request':
      return (
        <>
          <WithTimes text={warning.detail} />.{' '}
          {warning.url && <a href={warning.url} target="_blank" rel="noreferrer" className="whitespace-nowrap text-accent hover:underline">Open the pull request</a>}
        </>
      )
    case 'unmerged_work':
      return `${warning.detail}. The branch stays.`
    case 'messages':
      return warning.run_ids?.length
        ? <>{warning.count} {warning.count === 1 ? 'message' : 'messages'} from <Runs ids={warning.run_ids} onLeave={onLeave} /> {warning.count === 1 ? 'is' : 'are'} unanswered or arrived in the last 30 minutes.</>
        : `${warning.detail}.`
    case 'awaited':
      return <>Waiting for it to finish: <Runs ids={warning.run_ids ?? []} onLeave={onLeave} />. Closing wakes them with its outcome.</>
    case 'protected':
      if (run && self.id === run.member_id) return 'Protected. You can still close it as its owner.'
      if (self.role === 'admin') return 'Protected. You can still close it as an admin.'
      return `${warning.detail}.`
    case 'cleanup_pending':
      return `${warning.detail}. Cleanup keeps retrying after the close; the container may stay until it succeeds.`
    default:
      return `${warning.detail}.`
  }
}

function Runs({ ids, onLeave }: { ids: string[]; onLeave: () => void }) {
  const runs = useStore((s) => s.runs)
  const navigate = useStore((s) => s.navigate)
  return ids.map((id, i) => (
    <span key={id}>
      {i > 0 && ', '}
      <Button
        variant="link"
        className="h-auto p-0 text-ui-sm"
        onClick={() => {
          onLeave()
          navigate('run', { runId: id })
        }}
      >
        {runs[id] ? runLabel(runs[id]) : id}
      </Button>
    </span>
  ))
}

export function CloseConfirmation() {
  const pending = useStore((s) => s.closeConfirmation)
  const run = useStore((s) => (pending ? s.runs[pending.check.run_id] : undefined))
  const dismiss = useStore((s) => s.dismissCloseConfirmation)
  const perform = useCommandRunner()
  const gone = Boolean(pending && !run)
  useEffect(() => {
    if (gone) dismiss()
  }, [gone, dismiss])
  if (!pending || !run) return null
  return (
    <AlertDialog open onOpenChange={(open) => { if (!open) dismiss() }}>
      <AlertDialogContent className="max-w-[min(480px,calc(100%-2rem))]">
        <AlertDialogHeader className="min-w-0">
          <AlertDialogTitle>Close this session anyway?</AlertDialogTitle>
          <p className="truncate text-ui-sm font-medium" title={runLabel(run)}>{runLabel(run)}</p>
          <AlertDialogDescription>
            {pending.command.label}: records it as {pending.outcome} and closes the session.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="-mx-4 border-y px-3 py-3 empty:hidden sm:px-4">
          <CloseWarnings check={pending.check} run={run} onLeave={dismiss} className={warningScroll} />
        </div>
        <AlertDialogFooter className="flex-col sm:flex-row">
          {/* Radix AlertDialog focuses Cancel when the confirmation opens. */}
          <AlertDialogCancel className={focusRingShown}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              dismiss()
              if (closeState(useStore.getState().runs[run.id]) !== pending.asked) {
                toast.info(closeChangedText)
                return
              }
              void perform(pending.command)
            }}
          >
            Close anyway
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
