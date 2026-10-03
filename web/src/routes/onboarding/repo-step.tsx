// The Repository step: point a local clone at the workspace, then seed the
// workspace from it through link.repo, repo.push and repo.fast-forward.

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
import { desktopBridge } from '@/components/shell/title-bar'
import type { Api } from '@/lib/api'
import type { LinkStatus, Workspace } from '@/lib/types'
import { useStore } from '@/store'
import { useSelfRole, type Capability } from '@/store/hooks'
import type { OnboardingRepo } from '@/store/ui'
import { actionRow, pane } from '@/routes/onboarding/steps'

/**
 * Whether the typed path is rooted. All three shapes are accepted whatever
 * this machine is, because the check exists only to catch a plainly relative
 * path before it is sent: a POSIX gateway answers a Windows path with git's
 * own error, and the reverse. Gating on the running platform instead would
 * refuse the exact path the desktop shell's own folder dialog just returned.
 */
const rooted = (path: string) => /^(\/|\\\\|[A-Za-z]:[\\/])/.test(path)

const short = (commit: string) => commit.slice(0, 7)
const commits = (n: number) => `${n} commit${n === 1 ? '' : 's'}`

/**
 * The record to merge an in-flight push or fast-forward answer into: the one
 * as it stands now, so a request started from the same screen sees the
 * other's answer, but only while it is still the connection the request was
 * issued against. Re-pointing, or a change of workspace, leaves that answer
 * - and its error - belonging to a connection the step has left. It compares
 * the link id rather than the path, because reconnecting the same folder to
 * the same workspace is a new connection whose remote was written again.
 */
const stillLinked = (origin: OnboardingRepo) => {
  const current = useStore.getState().onboardingRepo
  return current && current.link === origin.link ? current : null
}

/**
 * The repository folders this gateway already knows - the linked clone and
 * every named profile's own - deduplicated, for the field's suggestions.
 */
const knownRepos = (status: LinkStatus | null): string[] => {
  const paths = [status?.repo, ...(status?.links ?? []).map((l) => l.repo)]
  return [...new Set(paths.filter((path): path is string => !!path))]
}

/**
 * The Repository step: point a local clone at the workspace. The gateway
 * adds the `aether` git remote and, where the repo.push verb is served,
 * compares the clone with the workspace and pushes only when the clone is
 * ahead, keeping git's own answer on the page; without the verb the push
 * stays a copy-paste command. A workspace that is ahead is answered by a
 * fast-forward of the clone. Either way the history is the user's: nothing
 * rewrites it, and a divergence is resolved by hand.
 */
export function RepoStep({
  client,
  caps,
  workspace,
  back,
  onNext,
  mirrored = false,
  sourcePending = true,
}: {
  client: Api
  caps: Capability
  workspace: Workspace | null
  back?: ReactNode
  onNext: () => void
  mirrored?: boolean
  sourcePending?: boolean
}) {
  // What the step settled lives in the UI slice, not here: walking back to
  // this step must not ask for a path that is already connected. A remote
  // written for another workspace is not this step's answer, so a workspace
  // the user changed their mind about leaves the form empty again.
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
  // The desktop shell browses this machine's filesystem for the user; a
  // browser tab has no such dialog and keeps the typed field alone.
  const chooseFolder = desktopBridge()?.chooseFolder
  // Where the chooser is not modal to the window - an out-of-process
  // xdg-desktop-portal one is not - the whole form stays live while a dialog
  // is open, so a second dialog is reachable and a late answer would land on
  // top of whatever was typed or submitted meanwhile. The form waits on the
  // dialog instead. It is deliberately component state: leaving the step and
  // coming back is then the way out of a chooser that died without
  // answering, rather than a button disabled for good.
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
  // Settled means the workspace and the clone agree: nothing is left to
  // push, so Continue is the primary action and the push offer is gone.
  const settled =
    forwarded !== null ||
    pushed?.state === 'pushed' ||
    pushed?.state === 'up-to-date'
  // `git push` is the command to run only while the two tips have not
  // parted: offering it to a clone the workspace has moved past is offering
  // the `! [rejected] main -> main` this comparison exists to prevent.
  const manual = !mirrored && !sourcePending && canWrite && (!pushed?.state || pushed.state === 'pushed')
  // The commands that resolve a divergence by hand, in the order they run.
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
      // The step stays put on success so git's own answer is readable:
      // "Everything up-to-date" and "[new branch]" mean different things,
      // and only git can tell them apart.
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
      // Cancelling answers with an empty string. It must leave both the
      // typed path and the error explaining it alone: the error is often
      // why the dialog was opened, and the user still needs to read it.
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
    return <section aria-label="Local repository" className="space-y-3 border-t py-3 text-sm">
      <h3 className="font-semibold">Link from the computer holding your clone</h3>
      <p>This hosted gateway cannot read your filesystem or use your SSH identity. Open the desktop app or run <code>aether gui</code> on that computer, connected to this server, then open this workspace's repository settings.</p>
      <p className="text-xs text-muted-foreground">Replace <code>&lt;server-address-or-id&gt;</code> with this server's SSH address (including its SSH port) or server ID from your administrator, not this page's HTTP address. This hosted gateway does not expose that connection target. Replace the absolute clone path below.</p>
      {sourcePending && <p className="text-sm text-muted-foreground">Source ownership is unconfirmed. You can link the clone, but base pushes are unavailable until local-only ownership is confirmed.</p>}
      {workspace && <pre className="overflow-auto whitespace-pre-wrap break-words bg-muted p-3 text-xs">{`aether link ${shellQuote('<server-address-or-id>')} --repo /absolute/path/to/clone --workspace ${shellQuote(workspace.id)}${canWrite && !mirrored && !sourcePending ? ` &&\ngit -C /absolute/path/to/clone push -u aether ${shellQuote(branch)}` : `\n${mirrored ? '# The mirrored base is server-owned; do not push it.' : '# Base pushes require write access and confirmed local-only ownership.'}`}`}</pre>}
      {back}
    </section>
  }

  return (
    <section
      aria-label="Repository"
      className="min-w-0 space-y-4 border-b border-border/70 py-4"
    >
      <div className="space-y-1">
        <h2 className="text-base font-semibold">Connect your local repository</h2>
        <p className="text-sm leading-6 text-muted-foreground">
          The gateway adds an <span className="font-mono">aether</span> git
          remote to a clone on the gateway's computer for <strong>{workspace?.name}</strong>, base <code>{branch}</code>.{' '}
          {canPush
            ? 'Aether can then push your base branch for you. The history stays yours.'
            : 'Linking does not publish to the upstream or grant credentials.'}
        </p>
      </div>
      {mirrored && <p className="text-sm text-muted-foreground">This workspace has a server-owned source. Link the clone to pull run branches; use Source control to verify or adopt the base instead of pushing it.</p>}
      {sourcePending && <p className="text-sm text-muted-foreground">Source ownership is unconfirmed. You can link the clone, but base pushes are unavailable until local-only ownership is confirmed.</p>}
      {remembered && !connected && <p className="text-sm text-muted-foreground">The saved connection is not confirmed as this gateway's current clone. Link the intended repository again. Each server profile keeps one current clone, not one per workspace.</p>}
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
                    variant="outline"
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
              {back}
            </div>
          </form>
          {repo.trim() !== '' && !absolute && (
            <p className="text-sm text-muted-foreground">
              The path must be absolute.
            </p>
          )}
          {error && (
            <p className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-sm text-state-failed" aria-live="polite">
              {error}
            </p>
          )}
        </>
      )}
      {connected && (
        <div className="min-w-0 space-y-4 border-t border-border/70 py-3">
          <p className="text-sm">
            Connected <span className="break-all font-mono">{connected.path}</span>.
            Remote <span className="break-all font-mono">{connected.remote.remote}</span>{' '}
            points at{' '}
            <span className="break-all font-mono">{connected.remote.url}</span>.{' '}
            {connected.remote.origin && (
              <>
                Runs push to{' '}
                <span className="break-all font-mono">{connected.remote.origin}</span>.{' '}
              </>
            )}
            {pushed?.state === 'pushed' && (
              <>
                Pushed <span className="font-mono">{pushed.branch}</span> to{' '}
                <span className="font-mono">{pushed.remote}</span>.
              </>
            )}
            {pushed?.state === 'up-to-date' && (
              <>
                Workspace already has{' '}
                <span className="font-mono">{pushed.branch}</span> at{' '}
                <span className="font-mono">
                  {short(pushed.workspace_commit)}
                </span>
                . Nothing to push.
              </>
            )}
            {!pushed && !mirrored && !sourcePending && canWrite && (
              <>
                Seed the workspace with{' '}
                <span className="font-mono">{branch}</span>:
              </>
            )}
          </p>
          {pushed?.state === 'behind' && !forwarded && caps.hasLocal('repo.fast-forward') && (
            <div className="space-y-3 border-l-2 border-state-waiting/60 bg-state-waiting/5 px-3 py-2" aria-live="polite">
              <p className="text-sm">
                The workspace is {commits(pushed.behind)} ahead of your clone.
              </p>
              <p className="text-xs text-muted-foreground">
                Your clone{' '}
                <span className="font-mono">{short(pushed.local_commit)}</span>{' '}
                - workspace{' '}
                <span className="font-mono">
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
                  <p className="text-xs text-state-failed">
                    The fast-forward failed:
                  </p>
                  <pre className={`border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 ${pane}`}>
                    {forwardError}
                  </pre>
                </div>
              )}
              <p className="text-xs text-muted-foreground">
                Fast-forward only. Your history is never merged or rewritten.
              </p>
            </div>
          )}
          {pushed?.state === 'diverged' && !mirrored && !sourcePending && canWrite && (
            <div className="space-y-3 border-l-2 border-state-needs-attention/60 bg-state-needs-attention/5 px-3 py-2" aria-live="polite">
              <p className="text-sm">
                Your clone and the workspace have both moved on:{' '}
                {commits(pushed.ahead)} here, {pushed.behind} there. Aether
                never force-pushes.
              </p>
              <p className="text-xs text-muted-foreground">
                Your clone{' '}
                <span className="font-mono">{short(pushed.local_commit)}</span>{' '}
                - workspace{' '}
                <span className="font-mono">
                  {short(pushed.workspace_commit)}
                </span>
                .
              </p>
              <p className="text-sm">Resolve it by hand, then push again:</p>
              <div className="flex min-w-0 items-start gap-2">
                <pre className={`min-w-0 flex-1 border border-border/70 bg-background ${pane}`}>
                  {resolveCmds}
                </pre>
                <Button
                  variant="outline"
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
            <p className="border-l-2 border-state-done/60 bg-state-done/5 px-3 py-2 text-sm text-state-done" aria-live="polite">
              {forwarded.current ? (
                <>
                  Fast-forwarded{' '}
                  <span className="font-mono">{forwarded.branch}</span> to{' '}
                  <span className="font-mono">{short(forwarded.commit)}</span>.
                </>
              ) : (
                <>
                  Updated <span className="font-mono">{forwarded.branch}</span>{' '}
                  to{' '}
                  <span className="font-mono">{short(forwarded.commit)}</span>.
                  Another branch is checked out, so the fast-forward left your
                  working tree alone.
                </>
              )}
              {forwarded.dirty &&
                ' The uncommitted changes you already had are still there.'}
            </p>
          )}
          {settled ? (
            // Git's own answer, open: "Everything up-to-date" and "[new
            // branch]" both mean success and say different things, and the
            // reader who needs that distinction is the one who would not
            // know to go looking for it.
            <Collapsible defaultOpen className="min-w-0 border-t border-border/70">
              <CollapsibleTrigger className="px-3 py-2 text-sm">
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
                    <p className="text-xs text-state-failed">
                      The push failed. Git said:
                    </p>
                    <pre className={`border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 ${pane}`}>
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
                <p className="text-xs text-muted-foreground">
                  {settled ? 'The same push, by hand:' : 'or run it yourself:'}
                </p>
              )}
              <div className="grid min-w-0 gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                <Input
                  ref={cmdRef}
                  readOnly
                  aria-label="Push command"
                  className="min-w-0 font-mono"
                  value={pushCmd}
                  onFocus={(e) => e.target.select()}
                />
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void copy(pushCmd)}
                >
                  {copied === pushCmd ? 'Copied' : 'Copy'}
                </Button>
              </div>
            </>
          )}
          <div className={actionRow}>
            <Button
              size="sm"
              variant={canPush && !settled ? 'outline' : 'default'}
              onClick={onNext}
            >
              Continue
            </Button>
            <Button size="sm" variant="outline" onClick={repoint}>
              Use a different repository
            </Button>
            {back}
          </div>
        </div>
      )}
    </section>
  )
}
