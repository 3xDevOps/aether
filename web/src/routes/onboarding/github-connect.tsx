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
import type { GitHubConnectResult } from '@/lib/types'
import { TerminalDock } from '@/routes/board/terminal-dock'
import type { Capability } from '@/store/hooks'

/**
 * The sub-screen name the Agents step opens this under. The step's other
 * sub-screens are named by harness, and `env.harnesses` reports only the
 * shipped registry names, none of which start with `@`.
 */
export const githubSubStep = '@github'

/** The login the member runs in their environment terminal. The signing
 * scope is what lets `github.connect` register the key it generates. */
export const githubLoginCommand =
  'gh auth login --hostname github.com --git-protocol https --web --scopes admin:ssh_signing_key'

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
    <section aria-label="Connect GitHub" className="space-y-3">
      <h2 className="text-sm font-medium">Connect GitHub</h2>
      <p className="text-sm text-muted-foreground">
        Runs push branches and open pull requests from the server as you.
        Commits are signed with a key kept in your environment home, which
        never leaves the server.
      </p>
      {connection && (
        <p className="text-sm">Connected as {connection.login}</p>
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
        className="space-y-3 rounded-md border p-4"
      >
        <p className="text-sm font-medium">GitHub connected</p>
        <p className="text-sm text-muted-foreground">
          Connected to GitHub as {connection.login}. Signing key{' '}
          {connection.fingerprint} is registered on your account.
        </p>
        <Button size="sm" onClick={onClose}>
          Close
        </Button>
      </section>
    )
  }

  return (
    <section
      aria-label="Connect GitHub"
      className={
        hasTerminal
          ? 'space-y-3 rounded-md border p-4'
          : 'max-w-md space-y-3 rounded-md border p-4'
      }
    >
      <p className="text-sm font-medium">Connect GitHub</p>
      {hasTerminal ? (
        <>
          <p className="text-sm text-muted-foreground">
            The login command is ready in your environment terminal:
          </p>
          <TerminalDock client={client} openOnMount initialLine={githubLoginCommand} />
          <p className="text-sm text-muted-foreground">
            Finish the device login in your browser, then return here.
          </p>
          <code className="block rounded-md bg-muted px-2 py-1 font-mono text-xs">
            {githubLoginCommand}
          </code>
        </>
      ) : (
        <>
          <p className="text-sm text-muted-foreground">
            Open your environment terminal and log in to GitHub there:
          </p>
          <code className="block rounded-md bg-muted px-2 py-1 font-mono text-xs">
            aether terminal
          </code>
          <pre className="overflow-x-auto rounded-md bg-muted p-2 font-mono text-xs">
            {githubLoginCommand}
          </pre>
          <p className="text-sm text-muted-foreground">
            Finish the device login in your browser, then finish the
            connection from a terminal:
          </p>
          <code className="block rounded-md bg-muted px-2 py-1 font-mono text-xs">
            aether github connect
          </code>
        </>
      )}
      {error && <p className="text-xs text-state-failed">{error}</p>}
      <Button type="button" size="sm" onClick={() => void connect()} disabled={busy}>
        {busy ? 'Connecting GitHub...' : "I've logged in"}
      </Button>
    </section>
  )
}
