// Local-machine settings: the link, the background sync daemon, image
// scaffolding, and the live overlay. All of it rides the /local/v1 verbs, so
// the whole route gates on daemon.status; a remote monitor gets an empty
// state pointing at `aether gui`, not a broken form. Every server refusal is
// shown verbatim.

import { Copy } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { runLabel } from '@/lib/status'
import type {
  DaemonInstallResult,
  DaemonStatusResult,
  ImageScaffoldResult,
} from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { SyncPanel } from '@/routes/run-sync'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

export function SettingsRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()

  if (!caps.hasLocal('daemon.status')) {
    return (
      <div className="flex h-full flex-col">
        <ViewHeader title="Settings" />
        <div className="flex flex-1 items-center justify-center p-4">
          <p className="max-w-md text-center text-sm text-muted-foreground">
            These settings manage this machine's link, sync daemon and image
            scaffolds, so they live in the desktop app or `aether gui`. This
            gateway is a remote monitor.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title="Settings" subtitle="this machine" />
      <div className="flex-1 space-y-6 overflow-y-auto p-4">
        <LinkCard client={client} />
        <DaemonCard client={client} />
        <ScaffoldCard client={client} />
        <OverlayCard client={client} />
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
    <section aria-label="Link" className="space-y-2 rounded-md border bg-card p-3">
      <h2 className="text-sm font-medium">Link</h2>
      {error && <p className="text-xs text-state-failed">{error}</p>}
      {serverConfigured && (
        <dl className="space-y-1 text-sm">
          <div className="flex gap-2">
            <dt className="text-muted-foreground">Server</dt>
            <dd className="font-mono">{link?.addr}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="text-muted-foreground">User</dt>
            <dd>{link?.user}</dd>
          </div>
          {link?.linked && (
            <div className="flex gap-2">
              <dt className="text-muted-foreground">Repository</dt>
              <dd className="font-mono">{link.repo}</dd>
            </div>
          )}
        </dl>
      )}
      {(link?.links?.length ?? 0) > 0 && (
        <div className="space-y-1 pt-1">
          <h3 className="text-xs font-medium text-muted-foreground">Servers</h3>
          <ul className="space-y-1 text-sm">
            {link?.links?.map((l) => {
              const active = l.name === (link?.active ?? '')
              return (
                <li key={l.name} className="flex items-center gap-2">
                  <span className="font-mono">{l.name}</span>
                  <span className="text-muted-foreground">{l.addr}</span>
                  {active ? (
                    <span className="text-xs text-muted-foreground">active</span>
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
            <p className="text-xs text-muted-foreground">{switchNote}</p>
          )}
        </div>
      )}
      {link && serverConfigured && !link.linked && (
        <p className="text-sm text-muted-foreground">
          No repository linked. Start onboarding or link a repository from a
          terminal.
        </p>
      )}
      {link && !serverConfigured && (
        <p className="text-sm text-muted-foreground">
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
      className="space-y-2 rounded-md border bg-card p-3"
    >
      <h2 className="text-sm font-medium">Sync daemon</h2>
      {error && <p className="text-xs text-state-failed">{error}</p>}
      {status?.installed && !installed && (
        <p className="text-sm">
          Installed at <span className="font-mono">{status.unit_path}</span>.
        </p>
      )}
      {installed && (
        <div className="space-y-2">
          <p className="text-sm">
            Installed at <span className="font-mono">{installed.unit_path}</span>.
          </p>
          <div className="flex gap-2">
            <input
              ref={noteRef}
              readOnly
              aria-label="Enable command"
              className="w-full rounded-md border bg-background px-2 py-1 font-mono text-sm"
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
          className="max-w-md space-y-2"
          onSubmit={(e) => {
            e.preventDefault()
            void install()
          }}
        >
          <label className="block space-y-1 text-xs text-muted-foreground">
            Server
            <input
              className={field}
              value={server}
              onChange={(e) => setServer(e.target.value)}
            />
          </label>
          <label className="block space-y-1 text-xs text-muted-foreground">
            Repository
            <input
              className={field}
              value={repo}
              onChange={(e) => setRepo(e.target.value)}
            />
          </label>
          <Button size="sm" type="submit" disabled={busy || !server || !repo}>
            Install
          </Button>
        </form>
      )}
    </section>
  )
}

/**
 * image.scaffold writes a Dockerfile or devcontainer into a repository and
 * never overwrites existing files; the answer lists what was written.
 */
function ScaffoldCard({ client }: { client: Api }) {
  const [repo, setRepo] = useState('')
  const [kind, setKind] = useState<'dockerfile' | 'devcontainer'>('dockerfile')
  const [written, setWritten] = useState<ImageScaffoldResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const scaffold = async () => {
    setBusy(true)
    setError(null)
    setWritten(null)
    try {
      setWritten(await client.localImageScaffold(repo, kind))
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section
      aria-label="Image scaffold"
      className="space-y-2 rounded-md border bg-card p-3"
    >
      <h2 className="text-sm font-medium">Image scaffold</h2>
      <form
        aria-label="Scaffold image"
        className="max-w-md space-y-2"
        onSubmit={(e) => {
          e.preventDefault()
          void scaffold()
        }}
      >
        <label className="block space-y-1 text-xs text-muted-foreground">
          Repository
          <input
            className={field}
            value={repo}
            onChange={(e) => setRepo(e.target.value)}
          />
        </label>
        <label className="block space-y-1 text-xs text-muted-foreground">
          Kind
          <select
            className={field}
            value={kind}
            onChange={(e) => setKind(e.target.value as 'dockerfile' | 'devcontainer')}
          >
            <option value="dockerfile">dockerfile</option>
            <option value="devcontainer">devcontainer</option>
          </select>
        </label>
        <Button size="sm" type="submit" disabled={busy || !repo}>
          Scaffold
        </Button>
      </form>
      {error && <p className="text-xs text-state-failed">{error}</p>}
      {written && (
        <ul aria-label="Written files" className="space-y-1 text-sm">
          {written.written.map((file) => (
            <li key={file} className="font-mono">
              {file}
            </li>
          ))}
          {written.written.length === 0 && (
            <li className="text-muted-foreground">
              Nothing written; the files already exist.
            </li>
          )}
        </ul>
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
      aria-label="Live overlay"
      className="space-y-2 rounded-md border bg-card p-3"
    >
      <h2 className="text-sm font-medium">Live overlay</h2>
      {live.length === 0 && (
        <p className="text-sm text-muted-foreground">No active runs.</p>
      )}
      {live.length > 0 && (
        <label className="block max-w-md space-y-1 text-xs text-muted-foreground">
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
        </label>
      )}
      {runID && <SyncPanel runID={runID} client={client} />}
    </section>
  )
}

registerRoute('settings', SettingsRoute)
