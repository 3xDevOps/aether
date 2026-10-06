import { useState } from 'react'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { api, type Api } from '@/lib/api'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import type { Run, RunGitStatusResult } from '@/lib/types'
import { CommitStep } from '@/routes/diff/publish-commit'
import { Diagnostics, InlineError } from '@/routes/diff/publish-parts'
import { PushStep } from '@/routes/diff/publish-push'
import { usePublish, type Publish } from '@/routes/diff/publish-state'
import { useStore } from '@/store'

/** Stays mounted while the run is open so a typed message or an uncertain
 * PR creation survives closing the dialog and switching views. */
export function PublishDialog({ run, client = api }: { run: Run; client?: Api }) {
  const [open, setOpen] = useState(false)
  const p = usePublish(run, client, open)
  const [step, setStep] = useState('commit')
  const wrapping = useStore((s) => s.diffWrap)
  const coarse = useMediaQuery(coarsePointer)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">Publish…</Button>
      </DialogTrigger>
      <DialogContent className="md:max-w-3xl">
        <DialogHeader>
          <DialogTitle>Publish</DialogTitle>
          <DialogDescription>
            Commit, push and open a pull request from the run's checkout with native Git, as the run's account.
          </DialogDescription>
        </DialogHeader>
        <Checkout p={p} />
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
    <section aria-label="Run checkout" className="grid min-w-0 gap-2 rounded-panel border border-seam bg-chrome p-3">
      <div className="flex min-w-0 flex-col items-start justify-between gap-3 sm:flex-row">
        {status ? <Facts status={status} /> : <p className="text-ui text-muted">{p.errors.status ? 'Native Git status is unavailable.' : 'Loading native Git status...'}</p>}
        <Button variant="secondary" size="sm" disabled={p.busy} onClick={() => void p.perform('status', p.refreshStatus)}>
          Refresh status
        </Button>
      </div>
      <InlineError>{p.errors.status}</InlineError>
      {status && !p.statusReady && !p.busy && (
        <p role="status" className="text-ui-sm text-state-needs-you">
          Showing the last known checkout state. Refresh the status before another change; a lost response does not establish
          whether an earlier action succeeded.
        </p>
      )}
      {status?.identity_error && <InlineError>{status.identity_error}</InlineError>}
      {status && <Diagnostics output={status.output} error={status.error} />}
    </section>
  )
}

function Facts({ status }: { status: RunGitStatusResult }) {
  return (
    <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-ui-sm">
      <Fact term="Branch" code>{status.branch || '(detached)'}</Fact>
      <Fact term="HEAD" code>{status.head || '(unborn)'}</Fact>
      <Fact term="Agent account">
        {status.account_name || status.account_member_id} <span className="font-code text-muted">{status.account_member_id}</span>
      </Fact>
      <Fact term="GitHub identity">{status.identity || 'Unavailable'}</Fact>
      <Fact term="Upstream" code>{status.upstream ? `${status.upstream.remote}/${status.upstream.branch}` : 'None'}</Fact>
    </dl>
  )
}

function Fact({ term, code, children }: { term: string; code?: boolean; children: React.ReactNode }) {
  return (
    <>
      <dt className="text-muted">{term}</dt>
      <dd className={code ? 'font-code break-all text-text' : 'break-words text-text'}>{children}</dd>
    </>
  )
}
