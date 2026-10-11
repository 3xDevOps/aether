import { useId } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'
import { Actual, Attest, Diagnostics, Heading, InlineError, Outcome } from '@/routes/diff/publish-parts'
import { stateLabel } from '@/routes/diff/publish-pr'
import type { Publish } from '@/routes/diff/publish-state'

/** Each button sits under one sentence saying exactly what it will do, from
 * the fields above it: that sentence is what the member confirms. */
export function PushStep({ p }: { p: Publish }) {
  const ids = useId()
  const status = p.status
  if (!status) return null
  const { pushTarget: push, target } = p
  const urls = status.remotes?.find((remote) => remote.name === push.remote)?.push_urls ?? []
  const pushReady = Object.values(push).every((value) => value.trim())
  const found = p.currentPRReview
  const pr = found?.result.pull_request
  const identity = found?.result.identity || status.identity
  const createNext = !!p.push?.pushed && !pr
  return (
    <div className="grid min-w-0 gap-4">
      <section aria-labelledby={`${ids}-push`} className="grid min-w-0 gap-3">
        <Heading id={`${ids}-push`}>Push branch</Heading>
        <fieldset disabled={p.busy} className="grid min-w-0 gap-3 sm:grid-cols-[minmax(0,3fr)_minmax(0,5fr)_minmax(0,4fr)]">
          <div className="grid min-w-0 gap-1">
            <Label htmlFor={`${ids}-remote`}>Push remote</Label>
            <Select
              value={push.remote}
              onValueChange={(remote) => {
                const next = status.remotes?.find((entry) => entry.name === remote)?.push_urls ?? []
                p.changePushTarget({ ...push, remote, repository: next.length === 1 ? next[0]! : '' })
              }}
            >
              <SelectTrigger id={`${ids}-remote`} className="min-w-0">
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
              <SelectTrigger id={`${ids}-url`} className="min-w-0" disabled={!urls.length}>
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
        <p className="text-ui-sm text-muted">
          {pushReady && p.expected ? (
            <>
              Pushes commit <Code>{p.expected.head.slice(0, 8)}</Code> to <Code>{push.repository}</Code> as branch{' '}
              <Code>{push.head_branch}</Code>. Never a force push.
            </>
          ) : push.remote ? (
            'Choose the exact URL and branch to push to.'
          ) : (
            'This workspace sets no checkout Origin, so nothing is chosen for you. Pick a remote, or add one with git in the run’s terminal.'
          )}
        </p>
        <div className="grid justify-items-start gap-1">
          <Button
            variant={createNext ? 'secondary' : 'primary'}
            disabled={p.busy || !p.canWrite || !p.caps.hasMethod('run.git.push') || !p.expected || !pushReady}
            onClick={() => void p.perform('push', p.pushBranch)}
          >
            Push branch
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
        <Heading id={`${ids}-pr`}>GitHub pull request</Heading>
        <fieldset disabled={p.busy || p.uncertain} className="grid min-w-0 gap-3 sm:grid-cols-2">
          <FormField label="PR repository (owner/name)">
            <Input value={target.repository} onChange={(event) => p.changeTarget('repository', event.target.value)} />
          </FormField>
          <FormField label="PR base branch">
            <Input value={target.base_branch} onChange={(event) => p.changeTarget('base_branch', event.target.value)} />
          </FormField>
          <FormField label="PR head repository (owner/name)">
            <Input value={target.head_repository} onChange={(event) => p.changeTarget('head_repository', event.target.value)} />
          </FormField>
          <FormField label="PR head branch">
            <Input value={target.head_branch} onChange={(event) => p.changeTarget('head_branch', event.target.value)} />
          </FormField>
        </fieldset>
        {p.uncertain && (
          <Callout tone="needs-you" role="alert">
            Creation outcome is uncertain. Keep this exact target and reconcile read-only; creation will not be retried.
          </Callout>
        )}
        {(!found || p.uncertain) && (
          <div className="grid justify-items-start gap-1">
            {p.action === 'discover' && !p.uncertain ? (
              <p role="status" className="text-ui-sm text-muted">Looking for an existing pull request…</p>
            ) : (
              <Button
                variant="secondary"
                disabled={p.busy || !p.expected || !p.targetComplete || !p.caps.hasMethod('run.pr.status')}
                onClick={() => void p.perform('discover', p.discoverPR)}
              >
                {p.uncertain ? 'Reconcile PR read-only' : 'Discover existing PR'}
              </Button>
            )}
            <InlineError>{p.errors.discover}</InlineError>
          </div>
        )}
        {found && (
          <Outcome label="Discovered pull request">
            <Actual actual={found.result.actual} />
            <Diagnostics output={found.result.output} error={found.result.error} />
            {pr ? (
              <div className="grid gap-0.5 break-all">
                <a className="text-accent underline-offset-2 hover:underline" href={pr.url} target="_blank" rel="noreferrer">
                  #{pr.number}: {pr.title}
                </a>
                <p>
                  {stateLabel(pr)} · {pr.head_repository}:{pr.head_branch} → {pr.repository}:{pr.base_branch}
                </p>
                <p className="text-muted">Its checks and review comments are under #{pr.number} in the run’s header.</p>
              </div>
            ) : (
              !found.result.error && <p>No existing PR for this exact repository, base and head.</p>
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
            {p.targetComplete && (
              <p className="text-ui-sm text-muted">
                Opens a pull request into <Code>{target.repository}:{target.base_branch}</Code> from{' '}
                <Code>{target.head_repository}:{target.head_branch}</Code>
                {identity ? <> as <strong className="font-medium text-text">{identity}</strong></> : null}. It is not merged from here.
              </p>
            )}
            <div className="grid justify-items-start gap-1">
              <Button
                variant={createNext ? 'primary' : 'secondary'}
                disabled={
                  p.busy || !p.canWrite || !p.caps.hasMethod('run.pr.create') || p.uncertain || !found || !!found.result.error ||
                  !found.result.identity || !p.title.trim()
                }
                onClick={() => void p.perform('create', p.createPR)}
              >
                Create pull request
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
      </section>
    </div>
  )
}
