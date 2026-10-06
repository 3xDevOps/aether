// `gh auth login` is a device flow that ends in a browser, so the screen types
// it into the environment terminal and hands the rest to `github.connect`.

import { useEffect, useState } from 'react'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { githubLoginCommand, typedLoginCommand } from '@/lib/github'
import type { GitHubConnectResult, GitHubProbeResult } from '@/lib/types'
import { TerminalDock } from '@/routes/board/terminal-dock'
import { pane } from '@/routes/onboarding/steps'
import { useStore } from '@/store'
import type { Capability } from '@/store/hooks'

/** Other sub-screens are named by harness, and no registry name starts with `@`. */
export const githubSubStep = '@github'

/** `connection` is set once this session's connect succeeded. */
export function GitHubSection({
  connection,
  onOpen,
}: {
  connection: GitHubConnectResult | null
  onOpen: () => void
}) {
  return (
    <section
      aria-label="Connect GitHub"
      className="min-w-0 space-y-3 border-t border-border/70 py-3"
    >
      <div className="space-y-1">
        <h3 className="text-base font-semibold">Connect GitHub</h3>
        <p className="text-sm leading-6 text-muted-foreground">
          Runs push branches and open pull requests from the server as you.
          Commits are signed with a key kept in your environment home, which
          never leaves the server.
        </p>
      </div>
      {connection && (
        <p className="border-l-2 border-state-done/60 bg-state-done/5 px-3 py-2 text-sm text-state-done">
          Connected in this session as {connection.login}
        </p>
      )}
      <Button size="sm" variant="secondary" onClick={onOpen}>
        Connect GitHub
      </Button>
    </section>
  )
}

export function GitHubConnect({
  client,
  caps,
  onConnected,
  onClose,
}: {
  client: Api
  caps: Capability
  onConnected: (connection: GitHubConnectResult) => void
  onClose: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [connection, setConnection] = useState<GitHubConnectResult | null>(null)
  const [probe, setProbe] = useState<GitHubProbeResult | null>(null)
  const [probeError, setProbeError] = useState<string | null>(null)
  const [probeAttempt, setProbeAttempt] = useState(0)
  const hasTerminal = caps.hasWS('terminal')
  const running = useStore((st) => st.envTerminal.status?.running ?? false)
  const terminalError = useStore((st) => st.envTerminal.statusError)

  // Older environments have no gh. The probe runs inside the container, so it
  // waits for the dock: an image pull can outlast a gateway control call.
  useEffect(() => {
    // A new container is a new answer, and the standing one describes a
    // container that is gone; clear it before the wait, not after.
    setProbe(null)
    setProbeError(null)
    if (!hasTerminal || !running) return
    let live = true
    void client.githubProbe().then(
      (result) => {
        if (live) setProbe(result)
      },
      (err) => {
        if (live) setProbeError(message(err))
      },
    )
    return () => {
      live = false
    }
  }, [client, hasTerminal, running, probeAttempt])

  const recheck = () => setProbeAttempt((attempt) => attempt + 1)

  const ghUsable = probe?.status === 'ok'
  const ghUnusable = probe !== null && probe.status !== 'ok'
  // Fail open, never silent, never on the member's behalf: a check that could
  // not run shows the command without running it. A refused write is not that.
  const checkFailed = probeError !== null || (terminalError !== null && !running)

  const connect = async () => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const result = await client.githubConnect()
      setConnection(result)
      onConnected(result)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  if (connection) {
    return (
      <section
        aria-label="Connect GitHub"
        className="min-w-0 space-y-4 border-l-2 border-state-done/60 bg-state-done/5 px-3 py-3"
      >
        <div className="space-y-1">
          <p className="text-base font-semibold text-state-done">GitHub connected</p>
          <p className="text-sm leading-6 text-muted-foreground">
            Connected to GitHub as {connection.login}. Signing key{' '}
            {connection.fingerprint} is registered on your account.
          </p>
        </div>
        <Button size="sm" onClick={onClose}>
          Close
        </Button>
      </section>
    )
  }

  // Without a terminal socket the whole flow, `github.connect` included, is the CLI's.
  if (!hasTerminal) {
    return (
      <section
        aria-label="Connect GitHub"
        className="min-w-0 max-w-2xl space-y-4 border-t border-border/70 py-3"
      >
        <div className="space-y-1">
          <p className="text-base font-semibold">Connect GitHub</p>
          <p className="text-sm leading-6 text-muted-foreground">
            Open your environment terminal and log in to GitHub there:
          </p>
        </div>
        <code className="block min-w-0 overflow-x-auto border border-border/70 bg-muted px-3 py-2 font-mono text-xs">
          aether terminal
        </code>
        <pre className="min-w-0 overflow-x-auto whitespace-pre-wrap border border-border/70 bg-muted p-3 font-mono text-xs">
          {githubLoginCommand}
        </pre>
        <p className="text-sm leading-6 text-muted-foreground">
          Finish the device login in your browser, then finish the
          connection from a terminal:
        </p>
        <code className="block min-w-0 overflow-x-auto border border-border/70 bg-muted px-3 py-2 font-mono text-xs">
          aether github connect
        </code>
      </section>
    )
  }

  return (
    <section
      aria-label="Connect GitHub"
      className="min-w-0 space-y-4 border-t border-border/70 py-3"
    >
      <div className="space-y-1">
        <p className="text-base font-semibold">Connect GitHub</p>
        <p className="text-sm leading-6 text-muted-foreground" role="status">
          {screenLine({ ghUsable, ghUnusable, checkFailed, running })}
        </p>
      </div>
      {ghUnusable && <GitHubCliRemedy probe={probe} />}
      {probeError && (
        <pre className={`border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-state-failed ${pane}`}>
          {probeError}
        </pre>
      )}
      {(ghUnusable || probeError !== null) && (
        <Button type="button" size="sm" variant="secondary" onClick={recheck}>
          Check again
        </Button>
      )}
      <TerminalDock
        client={client}
        openOnMount
        initialLine={ghUsable ? typedLoginCommand : undefined}
      />
      {(ghUsable || checkFailed) && (
        <>
          <p className="text-sm leading-6 text-muted-foreground">
            {checkFailed
              ? 'This is the login the check would have made sure your terminal could run; nothing has been typed into it.'
              : 'gh asks you to press Enter to open the browser, then reports that it could not open one; that is expected inside a container: press Enter, ignore the failure, and open the printed URL yourself with the one-time code. Then return here.'}
          </p>
          <code className="block min-w-0 overflow-x-auto whitespace-pre-wrap border border-border/70 bg-muted px-3 py-2 font-mono text-xs">
            {githubLoginCommand}
          </code>
        </>
      )}
      {error && (
        <pre className={`border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-state-failed ${pane}`}>
          {error}
        </pre>
      )}
      <Button
        type="button"
        size="sm"
        onClick={() => void connect()}
        disabled={busy || ghUnusable}
      >
        {busy ? 'Connecting GitHub...' : "I've logged in"}
      </Button>
    </section>
  )
}

/** The container wait is named on its own, because it is the long part. */
function screenLine({
  ghUsable,
  ghUnusable,
  checkFailed,
  running,
}: {
  ghUsable: boolean
  ghUnusable: boolean
  checkFailed: boolean
  running: boolean
}): string {
  if (ghUnusable) return 'Your environment terminal cannot run the login yet:'
  if (ghUsable) return 'The login command is ready in your environment terminal:'
  if (checkFailed) return 'Could not check your environment terminal for gh:'
  return running
    ? 'Checking your environment terminal for gh...'
    : 'Waiting for your environment terminal to start...'
}

/** Only called for a gh that cannot log in, so the last arm is the outdated one. */
function describeGitHubCli(probe: GitHubProbeResult): string {
  switch (probe.status) {
    case 'missing':
      return 'There is no gh in your environment terminal: it predates the standard image that ships one.'
    case 'broken':
      return 'gh is in your environment terminal but would not run.'
    default:
      // Only a version that was read can be judged old, so there is
      // always one to name here.
      return `gh ${probe.version}${probe.path ? ` at ${probe.path}` : ''} in your environment terminal cannot answer the login check; ${probe.minimum} is the oldest that can.`
  }
}

/** A container keeps the image it started from, so every remedy but a reset
 * ends in reopening the terminal. */
function GitHubCliRemedy({ probe }: { probe: GitHubProbeResult }) {
  // A member with a saved image owns the whole way out; the server never
  // sends an admin command alongside one.
  const savedIsTheProblem = !!probe.saved_image
  return (
    <>
      <p className="text-sm text-muted-foreground">{describeGitHubCli(probe)}</p>
      {probe.admin_remedy && (
        <>
          <p className="text-sm text-muted-foreground">
            The image is the server's standard one, so a server admin gets
            the server a newer one:
          </p>
          <CopyableCommand command={probe.admin_remedy} />
        </>
      )}
      {probe.remedy && (
        <p className="text-sm text-muted-foreground">
          {probe.path
            ? `That file is in your own environment home, so it comes first on PATH and survives every image. Remove it and the image's own gh takes over, or replace it with ${probe.minimum} or newer. In the terminal below:`
            : savedIsTheProblem
              ? `Install a current gh in the terminal below and press Save environment, or press Reset to standard - which removes your saved ${probe.saved_image}. From your own machine that reset is:`
              : `${probe.admin_remedy ? 'Then reopen' : 'Reopen'} your environment terminal with Stop environment below and open it again, because a container keeps the image it started from. From your own machine that is:`}
        </p>
      )}
      {probe.remedy && <CopyableCommand command={probe.remedy} />}
      {probe.detail && (
        <pre className={`border-l-2 border-state-failed/60 bg-state-failed/5 ${pane}`}>{probe.detail}</pre>
      )}
    </>
  )
}
