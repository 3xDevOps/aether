import { type ReactNode, useEffect, useId, useRef, useState } from 'react'
import { message } from '@/lib/format'
import { shellQuote } from '@/lib/shell'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { desktopBridge } from '@/components/shell/window-bar'
import type { Api } from '@/lib/api'
import type { LinkStatus, Workspace } from '@/lib/types'
import { useStore } from '@/store'
import { useSelfRole, type Capability } from '@/store/hooks'
import type { OnboardingRepo } from '@/store/ui'
import { actionRow, pane } from '@/routes/onboarding/layout'

/** Accepts every platform's shape: gating on this platform could refuse the
 * path the shell's folder dialog returned, and git reports a foreign one. */
const rooted = (path: string) => /^(\/|\\\\|[A-Za-z]:[\\/])/.test(path)

const short = (commit: string) => commit.slice(0, 7)
const commits = (n: number) => `${n} commit${n === 1 ? '' : 's'}`

/** The current record, but only while it is still the connection the request
 * was issued against. Compares the link id, not the path: reconnecting the
 * same folder is a new connection. */
const stillLinked = (origin: OnboardingRepo) => {
  const current = useStore.getState().onboardingRepo
  return current && current.link === origin.link ? current : null
}

const knownRepos = (status: LinkStatus | null): string[] => {
  const paths = [status?.repo, ...(status?.links ?? []).map((l) => l.repo)]
  return [...new Set(paths.filter((path): path is string => !!path))]
}

/** Nothing rewrites the user's history; a divergence is resolved by hand. */
export function RepoStep({
  client,
  caps,
  workspace,
  back,
  cancel,
  onNext,
  mirrored = false,
  sourcePending = true,
}: {
  client: Api
  caps: Capability
  workspace: Workspace | null
  back?: ReactNode
  /** Closes the link form; `back` sits with Continue once a clone is linked. */
  cancel?: ReactNode
  onNext: () => void
  mirrored?: boolean
  sourcePending?: boolean
}) {
  // In the UI slice so walking back does not ask for a connected path again.
  const remembered = useStore((s) => s.onboardingRepo)
  const setConnected = useStore((s) => s.setOnboardingRepo)
  const currentLink = useStore((s) => s.linkStatus)
  const [linkChecked, setLinkChecked] = useState(!remembered || currentLink?.repo === remembered.remote.repo)
  const connected =
    linkChecked && remembered && remembered.workspace === (workspace?.id ?? '') &&
      currentLink?.repo === remembered.remote.repo
      ? remembered
      : null
  const [repo, setRepo] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const setLinkStatus = useStore((s) => s.setLinkStatus)
  const chooseFolder = desktopBridge()?.chooseFolder
  // An out-of-process chooser (xdg-desktop-portal) is not modal, so the form
  // waits on it. Component state, so leaving the step escapes a dead chooser.
  const [picking, setPicking] = useState(false)
  const suggestions = knownRepos(useStore((s) => s.linkStatus))
  const fieldId = useId()
  const listId = useId()
  // The push runs on its own flag: a failed push must not disable the link
  // form the user may want to correct, and the reverse.
  const [pushing, setPushing] = useState(false)
  const [pushError, setPushError] = useState<string | null>(null)
  const [forwarding, setForwarding] = useState(false)
  const [forwardError, setForwardError] = useState<string | null>(null)
  // The command last copied, so the two Copy buttons cannot report each
  // other's success.
  const [copied, setCopied] = useState('')
  const cmdRef = useRef<HTMLInputElement>(null)
  const linkVersion = useRef(0)
  const role = useSelfRole()
  useEffect(() => {
    setBusy(false)
    const unsubscribe = useStore.subscribe((state, previous) => {
      if (state.identityKey === previous.identityKey && state.connectionEpoch === previous.connectionEpoch && state.route === previous.route) return
      linkVersion.current += 1
      setConnected(null)
      setLinkChecked(false)
      setRepo('')
      setBusy(false)
      setPushing(false)
      setForwarding(false)
      setError(null)
      setPushError(null)
      setForwardError(null)
    })
    return () => { linkVersion.current += 1; unsubscribe() }
  }, [client, workspace?.id, setConnected])
  useEffect(() => {
    if (!caps.hasLocal('link.status')) return
    let live = true
    const check = async () => {
      const version = linkVersion.current
      try {
        const status = await client.localLinkStatus()
        if (live && version === linkVersion.current) { setLinkStatus(status); setLinkChecked(true) }
      } catch (cause) {
        if (live && version === linkVersion.current) { setLinkChecked(false); setError(message(cause)) }
      }
    }
    void check()
    window.addEventListener('focus', check)
    return () => { live = false; window.removeEventListener('focus', check) }
  }, [client, caps, setLinkStatus])

  const verifyConnection = async (origin: OnboardingRepo, version: number) => {
    const status = await client.localLinkStatus()
    if (version !== linkVersion.current || !stillLinked(origin)) return
    setLinkStatus(status)
    if (!status.linked || status.repo !== origin.remote.repo) {
      throw new Error(`The gateway now uses ${status.repo || 'no clone'}. Link ${origin.path} to this workspace again before running Git operations.`)
    }
  }

  // Every run forks from the workspace's base branch, so that is the branch
  // to seed - not always `main`.
  const branch = workspace?.base_branch ?? 'main'
  const pushCmd = `git push -u aether ${shellQuote(branch)}`
  const canWrite = role === 'admin' || role === 'collaborator'
  const canPush = caps.hasLocal('repo.push') && canWrite && !mirrored && !sourcePending
  const pushAllowed = useRef(canPush)
  pushAllowed.current = canPush
  const absolute = rooted(repo.trim())
  const pushed = connected?.push ?? null
  const forwarded = connected?.fastForward ?? null
  const settled =
    forwarded !== null ||
    pushed?.state === 'pushed' ||
    pushed?.state === 'up-to-date'
  // A clone the workspace has moved past would only get `! [rejected]`.
  const manual = !mirrored && !sourcePending && canWrite && (!pushed?.state || pushed.state === 'pushed')
  const resolveCmds =
    pushed?.state === 'diverged'
      ? [
          `git fetch ${shellQuote(pushed.remote)} ${shellQuote(pushed.branch)}`,
          `git log --oneline --left-right ${shellQuote(`${pushed.branch}...${pushed.remote}/${pushed.branch}`)}`,
          `git rebase ${shellQuote(`${pushed.remote}/${pushed.branch}`)}`,
          `git push ${shellQuote(pushed.remote)} ${shellQuote(pushed.branch)}`,
        ].join('\n')
      : ''

  const link = async () => {
    if (busy || !workspace || !caps.hasLocal('link.repo')) return
    const path = repo.trim()
    let version = ++linkVersion.current
    setBusy(true)
    setError(null)
    try {
      const remote = await client.localLinkRepo(path, workspace.id)
      if (version !== linkVersion.current) return
      version = ++linkVersion.current
      const status = await client.localLinkStatus()
      if (version !== linkVersion.current) return
      setLinkStatus(status)
      if (!status.linked || status.repo !== remote.repo) {
        throw new Error(`The gateway now uses ${status.repo || 'no clone'}, not ${remote.repo}. Link the intended clone again before running Git operations.`)
      }
      if (version !== linkVersion.current) return
      setConnected({
        link: crypto.randomUUID(),
        workspace: workspace.id,
        path,
        remote,
        push: null,
        fastForward: null,
      })
      setLinkChecked(true)
      if (version === linkVersion.current && remote.origin) {
        const current = useStore.getState().workspaces[workspace.id]
        if (current) useStore.getState().upsertWorkspace({ ...current, origin: remote.origin })
      }
    } catch (err) {
      if (version === linkVersion.current) setError(message(err))
    } finally {
      if (version === linkVersion.current) setBusy(false)
    }
  }

  const push = async () => {
    if (!connected || !pushAllowed.current) return
    const origin = connected
    const version = linkVersion.current
    setPushing(true)
    setPushError(null)
    try {
      // Stay put on success: only git's answer tells "Everything up-to-date"
      // from "[new branch]".
      await verifyConnection(origin, version)
      if (version !== linkVersion.current || !stillLinked(origin) || !pushAllowed.current) return
      const result = await client.localRepoPush(origin.workspace)
      const current = stillLinked(origin)
      if (version === linkVersion.current && current) setConnected({ ...current, push: result })
    } catch (err) {
      if (version === linkVersion.current && stillLinked(origin)) {
        setPushError(message(err))
        setError(message(err))
      }
    } finally {
      if (version === linkVersion.current && stillLinked(origin)) setPushing(false)
    }
  }

  const fastForward = async () => {
    if (!connected) return
    const origin = connected
    const version = linkVersion.current
    setForwarding(true)
    setForwardError(null)
    try {
      await verifyConnection(origin, version)
      if (version !== linkVersion.current || !stillLinked(origin)) return
      const result = await client.localRepoFastForward(origin.workspace)
      const current = stillLinked(origin)
      if (version === linkVersion.current && current) setConnected({ ...current, fastForward: result })
    } catch (err) {
      if (version === linkVersion.current && stillLinked(origin)) {
        setForwardError(message(err))
        setError(message(err))
      }
    } finally {
      if (version === linkVersion.current && stillLinked(origin)) setForwarding(false)
    }
  }

  const pick = async () => {
    if (!chooseFolder) return
    setPicking(true)
    try {
      const picked = await chooseFolder()
      // Cancelling answers "" and must leave the path and its error alone: the
      // error is often why the dialog was opened.
      if (picked) {
        setRepo(picked)
        setError(null)
      }
    } catch (err) {
      setError(message(err))
    } finally {
      setPicking(false)
    }
  }

  const repoint = () => {
    linkVersion.current += 1
    setRepo(connected?.path ?? '')
    setConnected(null)
    setError(null)
    setPushError(null)
    setForwardError(null)
    // A push or fast-forward still in flight belongs to the clone being left
    // behind, so its answer is dropped: nothing will clear these later.
    setPushing(false)
    setForwarding(false)
  }

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(text)
    } catch {
      // Nothing to select when the command is the diverged block's <pre>.
      cmdRef.current?.focus()
      cmdRef.current?.select()
    }
  }

  if (!caps.hasLocal('link.repo')) {
    return <section aria-label="Local repository" className="flex flex-col gap-3 text-ui">
      <h4 className="font-medium">Link from the computer holding your clone</h4>
      <p>This hosted gateway cannot read your filesystem or use your SSH identity. Open the desktop app or run <code>aether gui</code> on that computer, connected to this server, then open this workspace's repository settings.</p>
      <p className="text-ui-sm text-muted">Replace <code>&lt;server-address-or-id&gt;</code> with this server's SSH address (including its SSH port) or server ID from your administrator, not this page's HTTP address. This hosted gateway does not expose that connection target. Replace the absolute clone path below.</p>
      {sourcePending && <p className="text-ui text-muted">Source ownership is unconfirmed. You can link the clone, but base pushes are unavailable until local-only ownership is confirmed.</p>}
      {workspace && <pre className="overflow-auto whitespace-pre-wrap break-words bg-chrome p-3 text-ui-sm">{`aether link ${shellQuote('<server-address-or-id>')} --repo /absolute/path/to/clone --workspace ${shellQuote(workspace.id)}${canWrite && !mirrored && !sourcePending ? ` &&\ngit -C /absolute/path/to/clone push -u aether ${shellQuote(branch)}` : `\n${mirrored ? '# The mirrored base is server-owned; do not push it.' : '# Base pushes require write access and confirmed local-only ownership.'}`}`}</pre>}
      {back}
    </section>
  }

  return (
    <section aria-label="Local repository" className="flex min-w-0 flex-col gap-3 self-stretch">
      {mirrored && <p className="text-ui text-muted">This workspace has a server-owned source. Link the clone to pull run branches; {role === 'admin' ? 'use Source control to verify or adopt the base instead of pushing it.' : 'ask an administrator to verify or adopt the base in Source control instead of pushing it.'}</p>}
      {sourcePending && <p className="text-ui text-muted">Source ownership is unconfirmed. You can link the clone, but base pushes are unavailable until local-only ownership is confirmed.</p>}
      {remembered && !connected && <p className="text-ui text-muted">The saved connection is not confirmed as this gateway's current clone. Link the intended repository again. Each server profile keeps one current clone, not one per workspace.</p>}
      {!connected && (
        <>
          <form
            className="min-w-0 max-w-3xl space-y-4"
            aria-label="Link repository"
            onSubmit={(e) => {
              e.preventDefault()
              void link()
            }}
          >
            <div className="space-y-1.5">
              <Label className="block" htmlFor={fieldId}>
                Repository path
              </Label>
              <div className="grid min-w-0 gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                <Input
                  id={fieldId}
                  className="min-w-0"
                  value={repo}
                  list={listId}
                  disabled={picking}
                  placeholder="/home/you/code/myproject"
                  onChange={(e) => setRepo(e.target.value)}
                />
                <datalist id={listId}>
                  {suggestions.map((path) => (
                    <option key={path} value={path} />
                  ))}
                </datalist>
                {chooseFolder && (
                  <Button
                    type="button"
                    size="sm"
                    variant="secondary"
                    disabled={picking}
                    onClick={() => void pick()}
                  >
                    Choose folder
                  </Button>
                )}
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="submit"
                size="sm"
                disabled={busy || picking || !absolute || !workspace}
              >
                Add remote
              </Button>
              {cancel}
            </div>
          </form>
          {repo.trim() !== '' && !absolute && (
            <p className="text-ui text-muted">
              The path must be absolute.
            </p>
          )}
          {error && (
            <p className="rounded-panel bg-state-failed-soft px-3 py-2 text-ui text-state-failed" aria-live="polite">
              {error}
            </p>
          )}
        </>
      )}
      {connected && (
        <div className="flex min-w-0 flex-col items-start gap-3">
          <p className="text-ui">
            Connected <span className="break-all font-code">{connected.path}</span>.
            Remote <span className="break-all font-code">{connected.remote.remote}</span>{' '}
            points at{' '}
            <span className="break-all font-code">{connected.remote.url}</span>.{' '}
            {connected.remote.origin && (
              <>
                Runs push to{' '}
                <span className="break-all font-code">{connected.remote.origin}</span>.{' '}
              </>
            )}
            {pushed?.state === 'pushed' && (
              <>
                Pushed <span className="font-code">{pushed.branch}</span> to{' '}
                <span className="font-code">{pushed.remote}</span>.
              </>
            )}
            {pushed?.state === 'up-to-date' && (
              <>
                Workspace already has{' '}
                <span className="font-code">{pushed.branch}</span> at{' '}
                <span className="font-code">
                  {short(pushed.workspace_commit)}
                </span>
                . Nothing to push.
              </>
            )}
            {!pushed && !mirrored && !sourcePending && canWrite && (
              <>
                Seed the workspace with{' '}
                <span className="font-code">{branch}</span>:
              </>
            )}
          </p>
          {pushed?.state === 'behind' && !forwarded && caps.hasLocal('repo.fast-forward') && (
            <div className="flex flex-col gap-2 rounded-panel bg-state-needs-you-soft px-3 py-2" aria-live="polite">
              <p className="text-ui">
                The workspace is {commits(pushed.behind)} ahead of your clone.
              </p>
              <p className="text-ui-sm text-muted">
                Your clone{' '}
                <span className="font-code">{short(pushed.local_commit)}</span>{' '}
                - workspace{' '}
                <span className="font-code">
                  {short(pushed.workspace_commit)}
                </span>
                .
              </p>
              <Button
                size="sm"
                disabled={forwarding}
                onClick={() => void fastForward()}
              >
                {forwarding ? 'Fast-forwarding...' : 'Fast-forward my clone'}
              </Button>
              {forwardError && (
                <div className="space-y-1">
                  <p className="text-ui-sm text-state-failed">
                    The fast-forward failed:
                  </p>
                  <pre className={`rounded-panel bg-state-failed-soft px-3 py-2 ${pane}`}>
                    {forwardError}
                  </pre>
                </div>
              )}
              <p className="text-ui-sm text-muted">
                Fast-forward only. Your history is never merged or rewritten.
              </p>
            </div>
          )}
          {pushed?.state === 'diverged' && !mirrored && !sourcePending && canWrite && (
            <div className="flex flex-col gap-2 rounded-panel bg-state-needs-you-soft px-3 py-2" aria-live="polite">
              <p className="text-ui">
                Your clone and the workspace have both moved on:{' '}
                {commits(pushed.ahead)} here, {pushed.behind} there. Aether
                never force-pushes.
              </p>
              <p className="text-ui-sm text-muted">
                Your clone{' '}
                <span className="font-code">{short(pushed.local_commit)}</span>{' '}
                - workspace{' '}
                <span className="font-code">
                  {short(pushed.workspace_commit)}
                </span>
                .
              </p>
              <p className="text-ui">Resolve it by hand, then push again:</p>
              <div className="flex min-w-0 items-start gap-2">
                <pre className={`min-w-0 flex-1 border border-seam bg-canvas ${pane}`}>
                  {resolveCmds}
                </pre>
                <Button
                  variant="secondary"
                  size="sm"
                  aria-label="Copy resolve commands"
                  onClick={() => void copy(resolveCmds)}
                >
                  {copied === resolveCmds ? 'Copied' : 'Copy'}
                </Button>
              </div>
            </div>
          )}
          {forwarded && (
            <p className="rounded-panel bg-state-done-soft px-3 py-2 text-ui text-state-done" aria-live="polite">
              {forwarded.current ? (
                <>
                  Fast-forwarded{' '}
                  <span className="font-code">{forwarded.branch}</span> to{' '}
                  <span className="font-code">{short(forwarded.commit)}</span>.
                </>
              ) : (
                <>
                  Updated <span className="font-code">{forwarded.branch}</span>{' '}
                  to{' '}
                  <span className="font-code">{short(forwarded.commit)}</span>.
                  Another branch is checked out, so the fast-forward left your
                  working tree alone.
                </>
              )}
              {forwarded.dirty &&
                ' The uncommitted changes you already had are still there.'}
            </p>
          )}
          {settled ? (
            // Open by default: whoever needs git's distinction would not look for it.
            <Collapsible defaultOpen className="min-w-0 self-stretch">
              <CollapsibleTrigger>
                What git did
              </CollapsibleTrigger>
              <CollapsibleContent>
                <pre className={pane}>
                  {[pushed?.output, forwarded?.output]
                    .map((out) => out?.trim())
                    .filter(Boolean)
                    .join('\n\n') || 'git printed nothing.'}
                </pre>
              </CollapsibleContent>
            </Collapsible>
          ) : (
            canPush && (
              <>
                <Button size="sm" disabled={pushing} onClick={() => void push()}>
                  {pushing ? 'Pushing...' : 'Push now'}
                </Button>
                {pushError && (
                  <div className="space-y-1">
                    <p className="text-ui-sm text-state-failed">
                      The push failed. Git said:
                    </p>
                    <pre className={`rounded-panel bg-state-failed-soft px-3 py-2 ${pane}`}>
                      {pushError}
                    </pre>
                  </div>
                )}
              </>
            )
          )}
          {manual && (
            <>
              {canPush && (
                <p className="text-ui-sm text-muted">
                  {settled ? 'The same push, by hand:' : 'or run it yourself:'}
                </p>
              )}
              <div className="grid min-w-0 gap-2 self-stretch sm:grid-cols-[minmax(0,1fr)_auto]">
                <Input
                  ref={cmdRef}
                  readOnly
                  aria-label="Push command"
                  className="min-w-0 font-code"
                  value={pushCmd}
                  onFocus={(e) => e.target.select()}
                />
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => void copy(pushCmd)}
                >
                  {copied === pushCmd ? 'Copied' : 'Copy'}
                </Button>
              </div>
            </>
          )}
          <div className={`${actionRow} self-stretch`}>
            <Button
              variant={canPush && !settled ? 'secondary' : 'primary'}
              onClick={onNext}
            >
              Continue
            </Button>
            <Button variant="secondary" onClick={repoint}>
              Use a different repository
            </Button>
            {back}
          </div>
        </div>
      )}
    </section>
  )
}
