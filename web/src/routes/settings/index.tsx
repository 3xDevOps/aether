// Local-machine settings: the link, the background sync daemon, and the live
// overlay. All of it rides the /local/v1 verbs, so the whole route gates on
// daemon.status; a remote monitor gets an empty state pointing at `aether gui`,
// not a broken form. Every server refusal is shown verbatim.

import { Copy } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import type {
  DaemonInstallResult,
  DaemonStatusResult,
  RepoSyncResult,
} from '@/lib/types'
import { field } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { SyncPanel } from '@/routes/run-sync'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

export function SettingsRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()

  if (!caps.hasLocal('daemon.status') && !caps.hasLocal('repo.sync')) {
    return (
      <div className="flex h-full flex-col">
        <ViewHeader title="Settings" />
        <div className="flex flex-1 items-center justify-center p-6">
          <div className="w-full max-w-lg rounded-lg border bg-card p-6 text-center shadow-sm">
            <p className="text-base font-medium">Machine settings are unavailable here</p>
            <p className="mt-2 text-sm leading-6 text-muted-foreground">
              These settings manage this machine's link and sync daemon, so they
              live in the desktop app or `aether gui`. This gateway is a remote
              monitor.
            </p>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title="Settings" subtitle="this machine" />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <main className="mx-auto grid w-full max-w-5xl gap-4 p-4 sm:p-6">
          {caps.hasLocal('link.status') && <LinkCard client={client} />}
          {caps.hasLocal('daemon.status') && <DaemonCard client={client} />}
          {caps.hasLocal('repo.sync') && <RepoSyncCard client={client} />}
          {caps.hasLocal('sync.status') && <OverlayCard client={client} />}
        </main>
      </div>
    </div>
  )
}

/**
 * What this machine is linked to. Checked on every mount - `aether link` may
 * have run in a terminal since - and mirrored into the store so the status
 * bar agrees.
 */
function LinkCard({ client }: { client: Api }) {
  const setLinkStatus = useStore((s) => s.setLinkStatus)
  const link = useStore((s) => s.linkStatus)
  const [error, setError] = useState<string | null>(null)
  const serverConfigured = link !== null && link.server_configured
  // The gateway's SSH identity is process-lifetime, so link.switch always
  // answers an instruction to restart; show it verbatim.
  const [switchNote, setSwitchNote] = useState<string | null>(null)

  const switchTo = (name: string) => {
    setSwitchNote(null)
    client.localLinkSwitch(name).catch((err) => setSwitchNote(message(err)))
  }

  useEffect(() => {
    let cancelled = false
    client
      .localLinkStatus()
      .then((status) => {
        if (!cancelled) setLinkStatus(status)
      })
      .catch((err) => {
        if (!cancelled) setError(message(err))
      })
    return () => {
      cancelled = true
    }
  }, [client, setLinkStatus])

  return (
    <section
      aria-label="Link"
      className="space-y-4 rounded-lg border bg-card p-4 shadow-sm sm:p-5"
    >
      <div className="space-y-1">
        <h2 className="text-base font-semibold">Link</h2>
        <p className="text-sm leading-6 text-muted-foreground">
          See the server and repository this machine is connected to.
        </p>
      </div>
      {error && (
        <p className="rounded-md border border-state-failed/30 bg-state-failed/5 p-3 text-sm text-state-failed">
          {error}
        </p>
      )}
      {serverConfigured && (
        <dl className="grid gap-3 rounded-md border bg-background p-4 text-sm sm:grid-cols-3">
          <div className="space-y-1">
            <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
              Server
            </dt>
            <dd className="font-mono">{link?.addr}</dd>
          </div>
          <div className="space-y-1">
            <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
              User
            </dt>
            <dd>{link?.user}</dd>
          </div>
          {link?.linked && (
            <div className="space-y-1 sm:col-span-1">
              <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                Repository
              </dt>
              <dd className="truncate font-mono" title={link.repo}>
                {link.repo}
              </dd>
            </div>
          )}
        </dl>
      )}
      {(link?.links?.length ?? 0) > 0 && (
        <div className="space-y-3 rounded-md border bg-background p-4">
          <h3 className="text-sm font-semibold">Saved servers</h3>
          <ul className="space-y-2 text-sm">
            {link?.links?.map((l) => {
              const active = l.name === (link?.active ?? '')
              return (
                <li
                  key={l.name}
                  className="flex flex-wrap items-center gap-2 rounded-md border bg-card px-3 py-2"
                >
                  <span className="font-mono">{l.name}</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {l.addr}
                  </span>
                  {active ? (
                    <span className="rounded-sm bg-state-done/10 px-2 py-1 text-xs text-state-done">
                      active
                    </span>
                  ) : (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => switchTo(l.name)}
                    >
                      Switch
                    </Button>
                  )}
                </li>
              )
            })}
          </ul>
          {switchNote && (
            <p className="rounded-md border bg-muted/50 p-3 text-sm text-muted-foreground">
              {switchNote}
            </p>
          )}
        </div>
      )}
      {link && serverConfigured && !link.linked && (
        <p className="rounded-md bg-muted/50 p-3 text-sm text-muted-foreground">
          No repository linked. Start onboarding or link a repository from a
          terminal.
        </p>
      )}
      {link && !serverConfigured && (
        <p className="rounded-md bg-muted/50 p-3 text-sm text-muted-foreground">
          No server configured. Run `aether link` in a terminal to get started.
        </p>
      )}
    </section>
  )
}

/**
 * The background sync daemon. daemon.status says whether the unit exists;
 * installing writes it and answers with the unit path and an enable note the
 * user will want in a terminal, hence the copy button (jsdom and older
 * engines have no navigator.clipboard; the fallback selects the text).
 */
function DaemonCard({ client }: { client: Api }) {
  const link = useStore((s) => s.linkStatus)
  const [status, setStatus] = useState<DaemonStatusResult | null>(null)
  const [installed, setInstalled] = useState<DaemonInstallResult | null>(null)
  const [server, setServer] = useState('')
  const [repo, setRepo] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)
  const noteRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    let cancelled = false
    client
      .localDaemonStatus()
      .then((s) => {
        if (!cancelled) setStatus(s)
      })
      .catch((err) => {
        if (!cancelled) setError(message(err))
      })
    return () => {
      cancelled = true
    }
  }, [client])

  // link.status lands after mount; prefill the untouched form from it.
  useEffect(() => {
    if (!link?.linked) return
    setServer((v) => v || link.addr)
    setRepo((v) => v || link.repo)
  }, [link])

  const install = async () => {
    setBusy(true)
    setError(null)
    try {
      setInstalled(await client.localDaemonInstall(server, repo))
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const copy = async () => {
    if (!installed) return
    try {
      await navigator.clipboard.writeText(installed.note)
      setCopied(true)
    } catch {
      noteRef.current?.focus()
      noteRef.current?.select()
    }
  }

  return (
    <section
      aria-label="Sync daemon"
      className="space-y-4 rounded-lg border bg-card p-4 shadow-sm sm:p-5"
    >
      <div className="space-y-1">
        <h2 className="text-base font-semibold">Sync daemon</h2>
        <p className="text-sm leading-6 text-muted-foreground">
          Keep the local mirror available for runs that need files on this
          machine.
        </p>
      </div>
      {error && (
        <p className="rounded-md border border-state-failed/30 bg-state-failed/5 p-3 text-sm text-state-failed">
          {error}
        </p>
      )}
      {status?.installed && !installed && (
        <p className="rounded-md border border-state-done/30 bg-state-done/5 p-3 text-sm">
          Installed at <span className="font-mono">{status.unit_path}</span>.
        </p>
      )}
      {installed && (
        <div className="space-y-3 rounded-md border border-state-done/30 bg-state-done/5 p-4">
          <p className="text-sm">
            Installed at <span className="font-mono">{installed.unit_path}</span>.
          </p>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              ref={noteRef}
              readOnly
              aria-label="Enable command"
              className="font-mono"
              value={installed.note}
              onFocus={(e) => e.target.select()}
            />
            <Button variant="outline" size="sm" onClick={() => void copy()}>
              <Copy />
              {copied ? 'Copied' : 'Copy'}
            </Button>
          </div>
        </div>
      )}
      {status && !status.installed && !installed && (
        <form
          aria-label="Install sync daemon"
          className="max-w-2xl space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            void install()
          }}
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <Label className="block space-y-1.5">
              Server
              <Input value={server} onChange={(e) => setServer(e.target.value)} />
            </Label>
            <Label className="block space-y-1.5">
              Repository
              <Input value={repo} onChange={(e) => setRepo(e.target.value)} />
            </Label>
          </div>
          <Button size="sm" type="submit" disabled={busy || !server || !repo}>
            {busy ? 'Installing...' : 'Install'}
          </Button>
        </form>
      )}
    </section>
  )
}

/**
 * Moves the linked repository's origin base branch into the server's
 * workspace branch and shows git's response without rewriting it.
 */
function RepoSyncCard({ client }: { client: Api }) {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const [result, setResult] = useState<RepoSyncResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const sync = async () => {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      setResult(await client.localRepoSync(workspaceID || undefined))
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section
      aria-label="Base branch"
      className="space-y-4 rounded-lg border bg-card p-4 shadow-sm sm:p-5"
    >
      <div className="space-y-1">
        <h2 className="text-base font-semibold">Base branch</h2>
        <p className="text-sm leading-6 text-muted-foreground">
          Fast-forwards the server&apos;s copy of the workspace base branch to
          your repository&apos;s origin remote.
        </p>
      </div>
      <Button size="sm" onClick={() => void sync()} disabled={busy}>
        {busy ? 'Syncing...' : 'Sync from origin'}
      </Button>
      {error && (
        <p className="rounded-md border border-state-failed/30 bg-state-failed/5 p-3 text-sm text-state-failed">
          {error}
        </p>
      )}
      {result && (
        <div className="space-y-3 rounded-md border border-state-done/30 bg-state-done/5 p-4 text-sm">
          <div>
            <span className="text-muted-foreground">Branch </span>
            <span className="font-mono">{result.branch}</span>
          </div>
          <pre className="overflow-x-auto whitespace-pre-wrap rounded-md border bg-background p-3 font-mono text-xs">
            {result.output}
          </pre>
        </div>
      )}
    </section>
  )
}


const terminal: Record<string, true> = {
  merged: true,
  abandoned: true,
  failed: true,
  interrupted: true,
}

/** Pick a live run and drive its sync overlay through the SyncPanel. */
function OverlayCard({ client }: { client: Api }) {
  const runs = useStore((s) => s.runs)
  const live = Object.values(runs).filter((r) => !terminal[r.status])
  const [runID, setRunID] = useState('')

  return (
    <section
      aria-labelledby="overlay-card-heading"
      className="space-y-4 rounded-lg border bg-card p-4 shadow-sm sm:p-5"
    >
      <div className="space-y-1">
        <h2 id="overlay-card-heading" className="text-base font-semibold">
          Mirror run files to your repository
        </h2>
        <p className="text-sm leading-6 text-muted-foreground">
          Pick a run and the local gateway mirrors its files into your linked
          repository as the agent works, so you can open them in your editor.
        </p>
      </div>
      {live.length === 0 && (
        <p className="rounded-md bg-muted/50 p-3 text-sm text-muted-foreground">
          No active runs.
        </p>
      )}
      {live.length > 0 && (
        <Label className="block max-w-md space-y-1 text-xs text-muted-foreground">
          Run
          <select
            className={field}
            value={runID}
            onChange={(e) => setRunID(e.target.value)}
          >
            <option value="">Pick a run</option>
            {live.map((r) => (
              <option key={r.id} value={r.id}>
                {runLabel(r)}
              </option>
            ))}
          </select>
        </Label>
      )}
      {runID && <SyncPanel runID={runID} client={client} />}
    </section>
  )
}

registerRoute('settings', SettingsRoute)
