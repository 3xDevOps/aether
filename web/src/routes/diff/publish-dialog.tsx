import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { create } from 'zustand'
import { RefreshCw } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { api, type Api } from '@/lib/api'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { cn } from '@/lib/utils'
import { CommitStep } from '@/routes/diff/publish-commit'
import { Diagnostics, InlineError } from '@/routes/diff/publish-parts'
import { PushStep } from '@/routes/diff/publish-push'
import type { ScopedRun } from '@/routes/diff/publish-pr'
import { usePublish, type Publish } from '@/routes/diff/publish-state'
import { useStore } from '@/store'
import { useHeaderPrimary } from '@/store/hooks'

const useRequests = create<Record<string, true>>(() => ({}))

/** Opens the run's Publish dialog, now or when the Changes view first mounts it. */
export function openPublish(runID: string) {
  useRequests.setState({ [runID]: true })
}

const Hosted = createContext(false)

function requestKeyOf(run: ScopedRun): string {
  return run.repo ? `${run.id}#${run.repo}` : run.id
}

/** Mounts one Publish dialog per repo of a multi-repo run, so a typed message or
 * an uncertain PR creation survives switching repo tabs. Inside it, each
 * `PublishDialog` is only the button that opens its repo's dialog. */
export function PublishHosts({ repos, children }: { repos: { run: ScopedRun; name?: string }[]; children: ReactNode }) {
  return (
    <Hosted.Provider value>
      {repos.map(({ run, name }) => (
        <PublishPanel key={requestKeyOf(run)} run={run} repoName={name} />
      ))}
      {children}
    </Hosted.Provider>
  )
}

/** Stays mounted while the run is open so a typed message or an uncertain
 * PR creation survives closing the dialog and switching views. `quiet` gives
 * the view's one filled button to another action. */
export function PublishDialog({ run, client = api, quiet = false, repoName }: { run: ScopedRun; client?: Api; quiet?: boolean; repoName?: string }) {
  const hosted = useContext(Hosted)
  useHeaderPrimary(hosted)
  if (!hosted) return <PublishPanel run={run} client={client} quiet={quiet} repoName={repoName} />
  return <Button size="sm" variant={quiet ? 'secondary' : 'primary'} onClick={() => openPublish(requestKeyOf(run))}>Publish…</Button>
}

function PublishPanel({ run, client = api, quiet, repoName }: { run: ScopedRun; client?: Api; quiet?: boolean; repoName?: string }) {
  const hosted = useContext(Hosted)
  useHeaderPrimary(!hosted)
  const [open, setOpen] = useState(false)
  const requestKey = requestKeyOf(run)
  const requested = useRequests((all) => all[requestKey])
  useEffect(() => {
    if (!requested) return
    setOpen(true)
    useRequests.setState(({ [requestKey]: _, ...rest }) => rest, true)
  }, [requested, requestKey])
  const p = usePublish(run, client, open)
  const [chosen, setStep] = useState<string>()
  // Nothing left to commit when the first status arrives: open on the push.
  const first = useRef<string>(undefined)
  if (p.status) first.current ??= p.status.changes?.length ? 'commit' : 'push'
  const step = chosen ?? first.current
  const wrapping = useStore((s) => s.diffWrap)
  const coarse = useMediaQuery(coarsePointer)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {!hosted && (
        <DialogTrigger asChild>
          <Button size="sm" variant={quiet ? 'secondary' : 'primary'}>Publish…</Button>
        </DialogTrigger>
      )}
      <DialogContent className="md:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{repoName ? `Publish ${repoName}` : 'Publish'}</DialogTitle>
          <DialogDescription>
            Commit, push and open a pull request from the session's checkout with native Git, as the session's account.
          </DialogDescription>
        </DialogHeader>
        <Checkout p={p} />
        {!p.canWrite && (
          <p className="text-ui-sm text-muted">You can read this checkout. Committing, pushing and opening a pull request need the collaborator role.</p>
        )}
        {p.status && (
          <Tabs value={step} onValueChange={setStep} className="grid min-w-0 gap-3">
            <TabsList look="segmented" className="justify-self-start">
              <TabsTrigger value="commit">1 · Commit</TabsTrigger>
              <TabsTrigger value="push">2 · Push and pull request</TabsTrigger>
            </TabsList>
            <TabsContent value="commit">
              <CommitStep p={p} wrap={wrapping ?? coarse} onContinue={() => setStep('push')} />
            </TabsContent>
            <TabsContent value="push">
              <PushStep p={p} />
            </TabsContent>
          </Tabs>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Checkout({ p }: { p: Publish }) {
  const status = p.status
  return (
    <section aria-label="Session checkout" className="grid min-w-0 gap-1">
      <div className="flex min-w-0 items-center gap-2 text-ui-sm text-muted">
        {status ? (
          <p className="min-w-0 flex-1 break-words">
            <span className="font-code text-text">{status.branch || '(detached)'}</span> at{' '}
            <span className="font-code text-text" title={status.head}>{status.head.slice(0, 8) || '(unborn)'}</span> · as{' '}
            {status.account_name || status.account_member_id} · GitHub <span className="text-text">{status.identity || 'login unavailable'}</span>
          </p>
        ) : (
          <p className="min-w-0 flex-1">{p.errors.status ? 'Native Git status is unavailable.' : 'Loading native Git status…'}</p>
        )}
        <Button variant="ghost" size="icon-sm" label="Refresh status" disabled={p.busy} onClick={() => void p.perform('status', p.refreshStatus)}>
          <RefreshCw className={cn(p.action === 'status' && 'animate-spin motion-reduce:animate-none')} />
        </Button>
      </div>
      <InlineError>{p.errors.status}</InlineError>
      {status && !p.statusReady && !p.busy && (
        <p role="status" className="text-ui-sm text-state-needs-you">
          Showing the last known checkout state. Refresh the status before another change; a lost response does not establish
          whether an earlier action succeeded.
        </p>
      )}
      {status?.identity_error && (
        <>
          <InlineError>{status.identity_error}</InlineError>
          <p className="text-ui-sm text-muted">
            A pull request is opened with this account’s GitHub login. Run <Code>gh auth login</Code> in the session’s terminal, then refresh the
            status.
          </p>
        </>
      )}
      {status && <Diagnostics output={status.output} error={status.error} />}
    </section>
  )
}
