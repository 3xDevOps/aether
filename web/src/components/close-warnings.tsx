import { useEffect, useState, type ReactNode } from 'react'
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
import { Button } from '@/components/ui/button'
import { readCloseCheck } from '@/lib/close-check'
import { useCommandRunner } from '@/lib/commands'
import { runLabel } from '@/lib/status'
import type { CloseOutcome, CloseWarning, Run, RunCloseCheck } from '@/lib/types'
import { useStore } from '@/store'

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

/** onLeave closes whatever dialog shows the warnings before a session link navigates away. */
export function CloseWarnings({ check, run, onLeave }: { check: RunCloseCheck; run: Run; onLeave: () => void }) {
  const listed = check.warnings.filter((warning) => warning.kind !== 'cleanup_pending' || !retentionShows(run, warning))
  return (
    <div className="flex flex-col gap-2 text-ui-sm">
      {check.error && <p role="alert" className="break-words text-state-failed">Could not check what closing affects: {check.error.message}</p>}
      {listed.length > 0 && (
        <ul aria-label="What closing affects" className="flex list-disc flex-col gap-1 pl-5">
          {listed.map((warning) => <li key={warning.kind + (warning.url ?? '')} className="break-words">{explain(warning, onLeave)}</li>)}
        </ul>
      )}
      <RunRetention run={run} />
    </div>
  )
}

/** RunRetention already says what a cleanup warning would, unless the check read a state the cached run does not have. */
function retentionShows(run: Run, warning: CloseWarning): boolean {
  return run.cleanup_error ? warning.detail === `Cleanup failed: ${run.cleanup_error}` : Boolean(run.cleanup_pending) && !warning.detail.startsWith('Cleanup failed')
}

function explain(warning: CloseWarning, onLeave: () => void): ReactNode {
  switch (warning.kind) {
    case 'pull_request':
      return (
        <>
          {warning.detail}.{' '}
          {warning.url && <a href={warning.url} target="_blank" rel="noreferrer" className="text-accent hover:underline">Open the pull request</a>}
        </>
      )
    case 'unmerged_work':
      return `${warning.detail}. The branch stays, but nothing merges it.`
    case 'messages':
      return `${warning.detail}. Another session may expect it to answer.`
    case 'awaited':
      return warning.subsession
        ? <>Subsession <Runs ids={[warning.subsession]} onLeave={onLeave} />: waiting for it to finish: <Runs ids={warning.run_ids ?? []} onLeave={onLeave} />. Closing wakes them with its outcome.</>
        : <>Waiting for it to finish: <Runs ids={warning.run_ids ?? []} onLeave={onLeave} />. Closing wakes them with its outcome.</>
    case 'subsessions':
      return <>Closes with it: <Runs ids={warning.run_ids ?? []} onLeave={onLeave} />.</>
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
        <AlertDialogHeader>
          <AlertDialogTitle>Close this session anyway?</AlertDialogTitle>
          <AlertDialogDescription>
            &quot;{runLabel(run)}&quot; - {pending.command.label} closes the session.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <CloseWarnings check={pending.check} run={run} onLeave={dismiss} />
        <AlertDialogFooter>
          {/* Radix AlertDialog focuses Cancel when the confirmation opens. */}
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              dismiss()
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
