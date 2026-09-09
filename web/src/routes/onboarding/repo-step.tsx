// The Repository step: point a local clone at the workspace, then seed the
// workspace from it through link.repo, repo.push and repo.fast-forward.

import { type ReactNode, useId, useRef, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { desktopBridge } from '@/components/shell/title-bar'
import type { Api } from '@/lib/api'
import type { LinkStatus, Workspace } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'
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
}: {
  client: Api
  caps: Capability
  workspace: Workspace | null
  back?: ReactNode
  onNext: () => void
}) {
  // What the step settled lives in the UI slice, not here: walking back to
  // this step must not ask for a path that is already connected. A remote
  // written for another workspace is not this step's answer, so a workspace
  // the user changed their mind about leaves the form empty again.
  const remembered = useStore((s) => s.onboardingRepo)
  const setConnected = useStore((s) => s.setOnboardingRepo)
  const connected =
    remembered && remembered.workspace === (workspace?.id ?? '')
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

  // Every run forks from the workspace's base branch, so that is the branch
  // to seed - not always `main`.
  const branch = workspace?.base_branch ?? 'main'
  const pushCmd = `git push -u aether ${branch}`
  const canPush = caps.hasLocal('repo.push')
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
  const manual = !pushed?.state || pushed.state === 'pushed'
  // The commands that resolve a divergence by hand, in the order they run.
  const resolveCmds =
    pushed?.state === 'diverged'
      ? [
          `git fetch ${pushed.remote} ${pushed.branch}`,
          `git log --oneline --left-right ${pushed.branch}...${pushed.remote}/${pushed.branch}`,
          `git rebase ${pushed.remote}/${pushed.branch}`,
          `git push ${pushed.remote} ${pushed.branch}`,
        ].join('\n')
      : ''

  const link = async () => {
    const path = repo.trim()
    setBusy(true)
    setError(null)
    try {
      setConnected({
        link: crypto.randomUUID(),
        workspace: workspace?.id ?? '',
        path,
        remote: await client.localLinkRepo(path, workspace?.id),
        push: null,
        fastForward: null,
      })
      void client.localLinkStatus().then(setLinkStatus).catch(() => undefined)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const push = async () => {
    if (!connected) return
    const origin = connected
    setPushing(true)
    setPushError(null)
    try {
      // The step stays put on success so git's own answer is readable:
      // "Everything up-to-date" and "[new branch]" mean different things,
      // and only git can tell them apart.
      const result = await client.localRepoPush(workspace?.id)
      const current = stillLinked(origin)
      if (current) setConnected({ ...current, push: result })
    } catch (err) {
      if (stillLinked(origin)) setPushError(message(err))
    } finally {
      if (stillLinked(origin)) setPushing(false)
    }
  }

  const fastForward = async () => {
    if (!connected) return
    const origin = connected
    setForwarding(true)
    setForwardError(null)
    try {
      const result = await client.localRepoFastForward(workspace?.id)
      const current = stillLinked(origin)
      if (current) setConnected({ ...current, fastForward: result })
    } catch (err) {
      if (stillLinked(origin)) setForwardError(message(err))
    } finally {
      if (stillLinked(origin)) setForwarding(false)
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

  return (
    <section aria-label="Repository" className="space-y-3">
      <h2 className="text-sm font-medium">Connect your repository</h2>
      <p className="text-sm text-muted-foreground">
        The gateway adds an <span className="font-mono">aether</span> git
        remote to a clone on this machine.{' '}
        {canPush
          ? 'Aether can then push your base branch for you - the history stays yours.'
          : 'Pushing stays manual - the history is yours.'}
      </p>
      {!connected && (
        <>
          <form
            className="flex items-end gap-3"
            aria-label="Link repository"
            onSubmit={(e) => {
              e.preventDefault()
              void link()
            }}
          >
            <div className="flex-1 space-y-1">
              <Label className="block" htmlFor={fieldId}>
                Repository path
              </Label>
              <div className="flex items-center gap-2">
                <Input
                  id={fieldId}
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
            <Button
              type="submit"
              size="sm"
              disabled={busy || picking || !absolute}
            >
              Add remote
            </Button>
            {back}
          </form>
          {repo.trim() !== '' && !absolute && (
            <p className="text-xs text-muted-foreground">
              The path must be absolute.
            </p>
          )}
          {error && (
            <p aria-live="polite" className="text-xs text-state-failed">
              {error}
            </p>
          )}
        </>
      )}
      {connected && (
        <div className="space-y-2">
          <p className="text-sm">
            Connected <span className="font-mono">{connected.path}</span>.
            Remote <span className="font-mono">{connected.remote.remote}</span>{' '}
            points at{' '}
            <span className="font-mono">{connected.remote.url}</span>.{' '}
            {connected.remote.origin && (
              <>
                Runs push to{' '}
                <span className="font-mono">{connected.remote.origin}</span>.{' '}
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
            {!pushed && (
              <>
                Seed the workspace with{' '}
                <span className="font-mono">{branch}</span>:
              </>
            )}
          </p>
          {pushed?.state === 'behind' && !forwarded && (
            <div className="space-y-2" aria-live="polite">
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
                  <pre className={`rounded-md border bg-card ${pane}`}>
                    {forwardError}
                  </pre>
                </div>
              )}
              <p className="text-xs text-muted-foreground">
                Fast-forward only. Your history is never merged or rewritten.
              </p>
            </div>
          )}
          {pushed?.state === 'diverged' && (
            <div className="space-y-2" aria-live="polite">
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
              <div className="flex items-start gap-2">
                <pre className={`flex-1 rounded-md border bg-card ${pane}`}>
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
            <p className="text-sm" aria-live="polite">
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
            <details open className="rounded-md border bg-card">
              <summary className={cn(focusRing, 'cursor-pointer px-3 py-2 text-sm')}>
                What git did
              </summary>
              <pre className={pane}>
                {[pushed?.output, forwarded?.output]
                  .map((out) => out?.trim())
                  .filter(Boolean)
                  .join('\n\n') || 'git printed nothing.'}
              </pre>
            </details>
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
                    <pre className={`rounded-md border bg-card ${pane}`}>
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
              <div className="flex gap-2">
                <Input
                  ref={cmdRef}
                  readOnly
                  aria-label="Push command"
                  className="font-mono"
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
