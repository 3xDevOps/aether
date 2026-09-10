// Connecting GitHub, as the Agents step's third section and its sub-screen.
//
// The half a server cannot do sits in the member's environment terminal:
// `gh auth login` is a device flow that ends in a browser. Everything after
// it is non-interactive, so the screen types the login command into the
// terminal and then hands the rest to `github.connect`.

import { useState } from 'react'
import { Button } from '@/components/ui/button'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import { githubLoginCommand } from '@/lib/github'
import type { GitHubConnectResult } from '@/lib/types'
import { TerminalDock } from '@/routes/board/terminal-dock'
import { pane } from '@/routes/onboarding/steps'
import type { Capability } from '@/store/hooks'

/**
 * The sub-screen name the Agents step opens this under. The step's other
 * sub-screens are named by harness, and `env.harnesses` reports only the
 * shipped registry names, none of which start with `@`.
 */
export const githubSubStep = '@github'

/** The closed section, between "Set up an agent" and the configuration
 * import. `connection` is set once this session's connect succeeded. */
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
      className="space-y-4 rounded-md border bg-background p-4 sm:p-5"
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
        <p className="rounded-md border border-state-done/30 bg-state-done/5 p-3 text-sm text-state-done">
          Connected in this session as {connection.login}
        </p>
      )}
      <Button size="sm" variant="outline" onClick={onOpen}>
        Connect GitHub
      </Button>
    </section>
  )
}

/** The open sub-screen: the login in the terminal, then the confirmation
 * that runs the non-interactive rest. */
export function GitHubConnect({
  client,
  caps,
  onConnected,
  onClose,
}: {
  client: Api
  caps: Capability
  /** Reports the connection to the step, which shows it once closed. */
  onConnected: (connection: GitHubConnectResult) => void
  onClose: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [connection, setConnection] = useState<GitHubConnectResult | null>(null)
  const hasTerminal = caps.hasWS('terminal')

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
        className="space-y-4 rounded-md border border-state-done/30 bg-state-done/5 p-4 sm:p-5"
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

  // Without a terminal socket there is nothing to type the login into, so
  // the whole flow - including the `github.connect` this screen would call -
  // is the CLI's.
  if (!hasTerminal) {
    return (
      <section
        aria-label="Connect GitHub"
        className="max-w-2xl space-y-4 rounded-md border bg-background p-4 sm:p-5"
      >
        <div className="space-y-1">
          <p className="text-base font-semibold">Connect GitHub</p>
          <p className="text-sm leading-6 text-muted-foreground">
            Open your environment terminal and log in to GitHub there:
          </p>
        </div>
        <code className="block overflow-x-auto rounded-md border bg-muted px-3 py-2 font-mono text-xs">
          aether terminal
        </code>
        <pre className="overflow-x-auto rounded-md border bg-muted p-3 font-mono text-xs">
          {githubLoginCommand}
        </pre>
        <p className="text-sm leading-6 text-muted-foreground">
          Finish the device login in your browser, then finish the
          connection from a terminal:
        </p>
        <code className="block overflow-x-auto rounded-md border bg-muted px-3 py-2 font-mono text-xs">
          aether github connect
        </code>
      </section>
    )
  }

  return (
    <section
      aria-label="Connect GitHub"
      className="space-y-4 rounded-md border bg-background p-4 sm:p-5"
    >
      <div className="space-y-1">
        <p className="text-base font-semibold">Connect GitHub</p>
        <p className="text-sm leading-6 text-muted-foreground">
          The login command is ready in your environment terminal:
        </p>
      </div>
      <TerminalDock client={client} openOnMount initialLine={githubLoginCommand} />
      <p className="text-sm leading-6 text-muted-foreground">
        gh asks you to press Enter to open the browser, then reports that it
        could not open one; that is expected inside a container: press Enter,
        ignore the failure, and open the printed URL yourself with the
        one-time code. Then return here.
      </p>
      <code className="block overflow-x-auto rounded-md border bg-muted px-3 py-2 font-mono text-xs">
        {githubLoginCommand}
      </code>
      {error && (
        <pre className={`rounded-md border border-state-failed/30 bg-state-failed/5 text-state-failed ${pane}`}>
          {error}
        </pre>
      )}
      <Button type="button" size="sm" onClick={() => void connect()} disabled={busy}>
        {busy ? 'Connecting GitHub...' : "I've logged in"}
      </Button>
    </section>
  )
}
