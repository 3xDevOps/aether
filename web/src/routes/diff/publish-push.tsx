import { useId } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'
import { PRFeedback } from '@/routes/diff/publish-feedback'
import { Actual, Attest, Diagnostics, Heading, InlineError, Outcome } from '@/routes/diff/publish-parts'
import type { Publish } from '@/routes/diff/publish-state'

export function PushStep({ p }: { p: Publish }) {
  const ids = useId()
  const status = p.status
  if (!status) return null
  const { pushTarget: push } = p
  const urls = status.remotes?.find((remote) => remote.name === push.remote)?.push_urls ?? []
  const pr = p.currentPRReview?.result.pull_request
  const prNext = !!p.currentPRReview
  return (
    <div className="grid min-w-0 gap-4">
      <section aria-labelledby={`${ids}-push`} className="grid min-w-0 gap-3">
        <div className="grid gap-0.5">
          <Heading id={`${ids}-push`}>Push branch</Heading>
          <p className="text-ui-sm text-muted">
            Select a configured remote's exact writable push URL. This never changes checkout Origin, the mirror source or its
            accepted base. Configure additional remotes using native Git in the run terminal.
          </p>
        </div>
        <fieldset disabled={p.busy} className="grid min-w-0 gap-3 sm:grid-cols-3">
          <div className="grid min-w-0 gap-1">
            <Label htmlFor={`${ids}-remote`}>Push remote</Label>
            <Select
              value={push.remote}
              onValueChange={(remote) => p.changePushTarget({ ...push, remote, repository: '' })}
            >
              <SelectTrigger id={`${ids}-remote`}>
                <SelectValue placeholder="Choose remote" />
              </SelectTrigger>
              <SelectContent>
                {status.remotes?.map((remote) => (
                  <SelectItem key={remote.name} value={remote.name}>
                    {remote.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid min-w-0 gap-1">
            <Label htmlFor={`${ids}-url`}>Writable push URL</Label>
            <Select value={push.repository} onValueChange={(repository) => p.changePushTarget({ ...push, repository })}>
              <SelectTrigger id={`${ids}-url`} disabled={!urls.length}>
                <SelectValue placeholder="Choose exact URL" />
              </SelectTrigger>
              <SelectContent>
                {urls.map((url) => (
                  <SelectItem key={url} value={url}>
                    {url}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <FormField label="Push head branch">
            <Input value={push.head_branch} onChange={(event) => p.changePushTarget({ ...push, head_branch: event.target.value })} />
          </FormField>
        </fieldset>
        <Attest checked={p.pushReviewed} disabled={p.busy || !p.expected} onChange={p.setPushReviewed}>
          I reviewed the run account, branch, HEAD and exact push destination above.
        </Attest>
        <div className="grid justify-items-start gap-1">
          <Button
            variant={prNext ? 'secondary' : 'primary'}
            disabled={p.busy || !p.canWrite || !p.caps.hasMethod('run.git.push') || !p.expected || !p.pushReviewed || !Object.values(push).every((value) => value.trim())}
            onClick={() => void p.perform('push', p.pushBranch)}
          >
            Push reviewed branch
          </Button>
          <InlineError>{p.errors.push}</InlineError>
        </div>
        {p.push && (
          <Outcome label="Push outcome">
            <p>Pushed: {p.push.pushed ? 'yes' : 'no'}</p>
            <p className="font-code break-all">
              {p.push.head} → {p.push.target.repository} · {p.push.target.head_branch}
            </p>
            <Actual actual={p.push.actual} />
            <Diagnostics output={p.push.output} error={p.push.error} />
            {p.push.pushed && <p className="text-muted">A later PR error does not undo this push.</p>}
          </Outcome>
        )}
      </section>
      <Separator />
      <section aria-labelledby={`${ids}-pr`} className="grid min-w-0 gap-3">
        <div className="grid gap-0.5">
          <Heading id={`${ids}-pr`}>GitHub pull request</Heading>
          <p className="text-ui-sm text-muted">
            PR repository/base and fork head are independent of the push remote. Enter owner/name, not a clone URL. Discover the
            exact existing PR before creating one. No merge, force push or branch switch is performed here.
          </p>
        </div>
        <fieldset disabled={p.busy || p.uncertain} className="grid min-w-0 gap-3 sm:grid-cols-2">
          <FormField label="PR repository (owner/name)">
            <Input value={p.target.repository} onChange={(event) => p.changeTarget('repository', event.target.value)} />
          </FormField>
          <FormField label="PR base branch">
            <Input value={p.target.base_branch} onChange={(event) => p.changeTarget('base_branch', event.target.value)} />
          </FormField>
          <FormField label="PR head repository (owner/name)">
            <Input value={p.target.head_repository} onChange={(event) => p.changeTarget('head_repository', event.target.value)} />
          </FormField>
          <FormField label="PR head branch">
            <Input value={p.target.head_branch} onChange={(event) => p.changeTarget('head_branch', event.target.value)} />
          </FormField>
        </fieldset>
        <div className="grid justify-items-start gap-1">
          <Button
            variant="secondary"
            disabled={p.busy || !p.expected || !p.targetComplete || !p.caps.hasMethod('run.pr.status')}
            onClick={() => void p.perform('discover', p.discoverPR)}
          >
            {p.uncertain ? 'Reconcile PR read-only' : 'Discover existing PR'}
          </Button>
          <InlineError>{p.errors.discover}</InlineError>
        </div>
        {p.uncertain && (
          <Callout tone="needs-you" role="alert">
            Creation outcome is uncertain. Keep this exact target and reconcile read-only; creation will not be retried.
          </Callout>
        )}
        {p.currentPRReview && (
          <Outcome label="Discovered pull request">
            <p className="break-all">
              Reviewed GitHub identity: <strong>{p.currentPRReview.result.identity || 'Unavailable'}</strong> · Agent account:{' '}
              {p.currentPRReview.result.account_member_id}
            </p>
            <Actual actual={p.currentPRReview.result.actual} />
            <Diagnostics output={p.currentPRReview.result.output} error={p.currentPRReview.result.error} />
            {pr ? (
              <div className="grid gap-0.5 break-all">
                <a className="text-accent underline-offset-2 hover:underline" href={pr.url} target="_blank" rel="noreferrer">
                  #{pr.number}: {pr.title}
                </a>
                <p>
                  {pr.state}
                  {pr.draft ? ' · Draft' : ''} · {pr.head_repository}:{pr.head_branch} → {pr.repository}:{pr.base_branch}
                </p>
                <p className="font-code">PR head: {pr.head_oid}</p>
              </div>
            ) : (
              !p.currentPRReview.result.error && <p>No existing PR for this exact repository, base and head.</p>
            )}
          </Outcome>
        )}
        {!pr && (
          <>
            <FormField label="PR title">
              <Input value={p.title} disabled={p.busy} onChange={(event) => p.setTitle(event.target.value)} />
            </FormField>
            <FormField label="PR description">
              <Textarea value={p.body} disabled={p.busy} onChange={(event) => p.setBody(event.target.value)} />
            </FormField>
            <Attest checked={p.draft} disabled={p.busy} onChange={p.setDraft}>
              Draft PR
            </Attest>
            <Attest
              checked={p.prReviewed}
              disabled={p.busy || !p.currentPRReview || !!p.currentPRReview.result.error || !p.currentPRReview.result.identity}
              onChange={p.setPRReviewed}
            >
              I reviewed this GitHub identity and the exact PR repository, base, fork and head.
            </Attest>
            <div className="grid justify-items-start gap-1">
              <Button
                variant={prNext ? 'primary' : 'secondary'}
                disabled={
                  p.busy || !p.canWrite || !p.caps.hasMethod('run.pr.create') || p.uncertain || !p.currentPRReview ||
                  !!p.currentPRReview.result.error || !p.prReviewed || !p.title.trim()
                }
                onClick={() => void p.perform('create', p.createPR)}
              >
                Create reviewed PR
              </Button>
              <InlineError>{p.errors.create}</InlineError>
            </div>
          </>
        )}
        {p.creation && (
          <Outcome label="PR creation outcome">
            <p>
              Created: {p.creation.created ? 'yes' : 'no'} · Reconciled: {p.creation.reconciled ? 'yes' : 'no'}
            </p>
            <Actual actual={p.creation.actual} />
            <Diagnostics output={p.creation.output} error={p.creation.error} />
          </Outcome>
        )}
        <div className="grid justify-items-start gap-1">
          <Button
            variant="secondary"
            disabled={p.busy || !p.expected || !p.targetComplete || !p.caps.hasMethod('run.pr.feedback')}
            onClick={() => void p.perform('feedback', p.refreshFeedback)}
          >
            Refresh PR feedback
          </Button>
          <InlineError>{p.errors.feedback}</InlineError>
        </div>
        {p.feedback && <PRFeedback p={p} feedback={p.feedback} />}
      </section>
    </div>
  )
}
