import { useEffect, useId, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Actual, Attest, Diagnostics, InlineError, Outcome } from '@/routes/diff/publish-parts'
import { stateLabel } from '@/routes/diff/publish-pr'
import type { Publish } from '@/routes/diff/publish-state'
import { githubRepository } from '@/routes/diff/publish-target'

/** Why no push sentence can be written, which also holds the push back. */
function undetermined(p: Publish): string | null {
  const push = p.pushTarget
  if (!p.status?.remotes?.length) return 'This checkout has no remote. Add one with git in the run’s terminal, then refresh the status.'
  if (!push.remote) return 'This workspace sets no checkout Origin, so no destination is chosen for you. Pick a remote, or add one with git in the run’s terminal.'
  if (!push.repository) return `Remote ${push.remote} has more than one push URL. Choose the exact one.`
  if (!push.head_branch.trim()) return p.status.detached ? 'The checkout is on a detached HEAD. Name the branch to push this commit as.' : 'Name the branch to push to.'
  return null
}

/** Nothing is pushed or opened unless the sentence naming its destination is on screen. */
export function PushStep({ p }: { p: Publish }) {
  const ids = useId()
  const status = p.status
  const found = p.currentPRReview
  const gap = undetermined(p)
  const refused = Boolean(
    p.errors.push || (p.push && !p.push.pushed) || p.errors.discover || found?.result.error || p.errors.create || p.creation?.error,
  )
  const opened = useRef(false)
  if (gap || refused) opened.current = true
  const [toggled, setToggled] = useState<boolean>()
  const pushOutcome = useRef<HTMLDivElement>(null)
  const creationOutcome = useRef<HTMLDivElement>(null)
  useEffect(() => {
    pushOutcome.current?.scrollIntoView({ block: 'nearest' })
  }, [p.push])
  useEffect(() => {
    creationOutcome.current?.scrollIntoView({ block: 'nearest' })
  }, [p.creation])
  if (!status) return null

  const { pushTarget: push, target } = p
  const urls = status.remotes?.find((remote) => remote.name === push.remote)?.push_urls ?? []
  const pr = found?.result.pull_request
  const identity = found?.result.identity || status.identity
  const opens = !pr && p.targetComplete
  const canPush = !p.busy && p.canWrite && p.caps.hasMethod('run.git.push') && !!p.expected && !gap
  const canOpen =
    !p.busy && p.canWrite && p.caps.hasMethod('run.pr.create') && !p.uncertain && !!found && !found.result.error && !!found.result.identity &&
    !!p.title.trim()
  const pushOnly = () => void p.perform('push', p.pushBranch)

  return (
    <div className="grid min-w-0 gap-4">
      <section aria-label="Destination" className="grid min-w-0 gap-2 text-ui text-text">
        {gap ? (
          <p>{gap}</p>
        ) : (
          <p>
            Pushes commit <Code>{status.head.slice(0, 8)}</Code> to <Code>{push.repository}</Code> as branch <Code>{push.head_branch}</Code>. Never
            a force push.
          </p>
        )}
        <InlineError>{p.errors.push}</InlineError>
        {p.push && (
          <div ref={pushOutcome}>
            <Outcome label="Push outcome">
              <p>Pushed: {p.push.pushed ? 'yes' : 'no'}</p>
              <p className="font-code break-all">
                {p.push.head} → {p.push.target.repository} · {p.push.target.head_branch}
              </p>
              <Actual actual={p.push.actual} />
              <Diagnostics output={p.push.output} error={p.push.error} />
              {p.push.pushed && <p className="text-muted">A later PR error does not undo this push.</p>}
            </Outcome>
          </div>
        )}
        {pr ? (
          <div className="grid gap-0.5 break-words">
            <a className="justify-self-start text-accent underline-offset-2 hover:underline" href={pr.url} target="_blank" rel="noreferrer">
              #{pr.number}: {pr.title}
            </a>
            <p className="text-ui-sm text-muted">
              {stateLabel(pr)} · {pr.head_repository}:{pr.head_branch} → {pr.repository}:{pr.base_branch}. Its checks and review comments are
              under #{pr.number} in the run’s header.
            </p>
          </div>
        ) : p.targetComplete ? (
          <p>
            Opens a pull request into <Code>{target.repository}:{target.base_branch}</Code> from{' '}
            <Code>{target.head_repository}:{target.head_branch}</Code>
            {identity ? <> as <strong className="font-medium">{identity}</strong></> : null}. It is not merged from here.
          </p>
        ) : (
          push.repository && !githubRepository(push.repository) && (
            <p className="text-ui-sm text-muted">
              No pull request is filled in: <Code>{push.repository}</Code> is not a github.com repository.
            </p>
          )
        )}
        {p.action === 'discover' && !p.uncertain && <p role="status" className="text-ui-sm text-muted">Looking for an existing pull request…</p>}
        <InlineError>{p.errors.discover}</InlineError>
        {found && (
          <>
            <Actual actual={found.result.actual} />
            <Diagnostics output={found.result.output} error={found.result.error} />
          </>
        )}
        <InlineError>{p.errors.create}</InlineError>
        {p.creation && (
          <div ref={creationOutcome}>
            <Outcome label="PR creation outcome">
              <p>
                Created: {p.creation.created ? 'yes' : 'no'} · Reconciled: {p.creation.reconciled ? 'yes' : 'no'}
              </p>
              <Actual actual={p.creation.actual} />
              <Diagnostics output={p.creation.output} error={p.creation.error} />
            </Outcome>
          </div>
        )}
        <Collapsible open={toggled ?? opened.current} onOpenChange={setToggled} className="min-w-0">
          <CollapsibleTrigger>
            <span className="text-ui-sm text-muted">Change destination</span>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-1 grid min-w-0 gap-3 rounded-panel border border-seam bg-chrome p-3">
              <fieldset disabled={p.busy} className="grid min-w-0 gap-3 sm:grid-cols-[minmax(0,3fr)_minmax(0,5fr)_minmax(0,4fr)]">
                <div className="grid min-w-0 gap-1">
                  <Label htmlFor={`${ids}-remote`}>Push remote</Label>
                  <Select
                    value={push.remote}
                    onValueChange={(remote) => {
                      const next = status.remotes?.find((entry) => entry.name === remote)?.push_urls ?? []
                      p.changePushTarget({ remote, repository: next.length === 1 ? next[0]! : '' })
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
                  <Select value={push.repository} onValueChange={(repository) => p.changePushTarget({ repository })}>
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
                  <Input value={push.head_branch} onChange={(event) => p.changePushTarget({ head_branch: event.target.value })} />
                </FormField>
              </fieldset>
              <fieldset aria-label="GitHub pull request" disabled={p.busy || p.uncertain} className="grid min-w-0 gap-3 sm:grid-cols-2">
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
              {!found && !p.uncertain && p.action !== 'discover' && (
                <div>
                  <Button
                    variant="secondary"
                    disabled={p.busy || !p.expected || !p.targetComplete || !p.caps.hasMethod('run.pr.status')}
                    onClick={() => void p.perform('discover', p.discoverPR)}
                  >
                    Discover existing PR
                  </Button>
                </div>
              )}
              {found && !pr && !found.result.error && <p className="text-ui-sm text-muted">No existing PR for this exact repository, base and head.</p>}
              <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-ui-sm">
                <dt className="text-muted">HEAD</dt>
                <dd className="font-code break-all">{status.head || '(unborn)'}</dd>
                <dt className="text-muted">Agent account</dt>
                <dd className="break-words">
                  {status.account_name || status.account_member_id} <span className="font-code text-muted">{status.account_member_id}</span>
                </dd>
                {status.upstream && (
                  <>
                    <dt className="text-muted">Upstream</dt>
                    <dd className="font-code break-all">
                      {status.upstream.remote}/{status.upstream.branch}
                    </dd>
                  </>
                )}
              </dl>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </section>
      {opens && (
        <div className="grid min-w-0 gap-3">
          <FormField label="PR title" help={p.title.trim() ? undefined : 'A pull request needs a title.'}>
            <Input value={p.title} disabled={p.busy} onChange={(event) => p.setTitle(event.target.value)} />
          </FormField>
          <FormField label="PR description">
            <Textarea value={p.body} disabled={p.busy} onChange={(event) => p.setBody(event.target.value)} />
          </FormField>
          <Attest checked={p.draft} disabled={p.busy} onChange={p.setDraft}>
            Draft PR
          </Attest>
        </div>
      )}
      {p.uncertain && (
        <Callout tone="needs-you" role="alert">
          Creation outcome is uncertain. Keep this exact target and reconcile read-only; creation will not be retried.
        </Callout>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {p.uncertain ? (
          <Button disabled={p.busy || !p.expected || !p.caps.hasMethod('run.pr.status')} onClick={() => void p.perform('discover', p.discoverPR)}>
            Reconcile PR read-only
          </Button>
        ) : !opens ? (
          <Button disabled={!canPush} onClick={pushOnly}>
            Push branch
          </Button>
        ) : p.pushed || gap ? (
          <Button disabled={!canOpen} onClick={() => void p.perform('create', p.createPR)}>
            Open pull request
          </Button>
        ) : (
          <Button disabled={!canPush || !canOpen} onClick={() => void p.perform('push', p.pushAndOpen)}>
            Push and open pull request
          </Button>
        )}
        {(opens || p.uncertain) && (
          <Button variant="secondary" disabled={!canPush} onClick={pushOnly}>
            Push only
          </Button>
        )}
      </div>
    </div>
  )
}
