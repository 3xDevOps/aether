import { useEffect, useState } from 'react'
import { CopyableCommand } from '@/components/copyable-command'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Api } from '@/lib/api'
import { linkTarget, message } from '@/lib/format'
import { runLabel } from '@/lib/status'
import type { DaemonInstallResult, DaemonStatusResult } from '@/lib/types'
import { SyncPanel } from '@/routes/run-sync'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

export function ThisComputerSection({ client }: { client: Api }) {
  const caps = useCapability()
  if (!caps.hasLocal('link.status') && !caps.hasLocal('daemon.status') && !caps.hasLocal('sync.status')) return null
  return (
    <SettingsSection title="This computer">
      {caps.hasLocal('link.status') && <LinkRows client={client} />}
      {caps.hasLocal('daemon.status') && <DaemonRow client={client} />}
      {caps.hasLocal('sync.status') && <MirrorRow client={client} />}
    </SettingsSection>
  )
}

/** Read on every mount: `aether link` may have run in a terminal since. */
function LinkRows({ client }: { client: Api }) {
  const link = useStore((s) => s.linkStatus)
  const setLinkStatus = useStore((s) => s.setLinkStatus)
  const navigate = useStore((s) => s.navigate)
  const [error, setError] = useState<string | null>(null)
  // The gateway's SSH identity lives as long as its process, so link.switch
  // always answers with a restart instruction; it is shown verbatim.
  const [switchNote, setSwitchNote] = useState<string | null>(null)
  const configured = link?.server_configured === true

  useEffect(() => {
    let live = true
    client.localLinkStatus().then(
      (status) => live && setLinkStatus(status),
      (err: unknown) => live && setError(message(err)),
    )
    return () => {
      live = false
    }
  }, [client, setLinkStatus])

  const switchTo = (name: string) => {
    setSwitchNote(null)
    client.localLinkSwitch(name).catch((err: unknown) => setSwitchNote(message(err)))
  }

  const saved = link?.links ?? []
  return (
    <>
      <SettingRow
        label="Server"
        help={
          !link
            ? 'Checking the link…'
            : configured
              ? <>Connected to <Code>{linkTarget(link)}</Code> as {link.user}.</>
              : <>No server yet. Run <Code>aether link</Code> in a terminal.</>
        }
      >
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
      </SettingRow>
      {configured && (
        <SettingRow
          label="Repository"
          help={link.linked ? <Code className="break-all">{link.repo}</Code> : 'No clone linked to this server.'}
          control={<Button size="sm" variant="secondary" onClick={() => navigate('workspaces')}>Manage workspaces</Button>}
        />
      )}
      {saved.length > 0 && (
        <SettingRow label="Saved servers" help="Servers this computer has linked to.">
          <ul aria-label="Saved servers" className="flex min-w-0 flex-col">
            {saved.map((entry) => (
              <li key={entry.name} className="flex min-h-8 min-w-0 items-center gap-2 text-ui">
                <span className="font-code">{entry.name}</span>
                <span className="min-w-0 flex-1 truncate text-muted">{entry.addr}</span>
                {entry.name === (link?.active ?? '')
                  ? <Badge tone="done">Active</Badge>
                  : <Button size="sm" variant="secondary" onClick={() => switchTo(entry.name)}>Switch</Button>}
              </li>
            ))}
          </ul>
          {switchNote && <Callout tone="neutral">{switchNote}</Callout>}
        </SettingRow>
      )}
    </>
  )
}

function DaemonRow({ client }: { client: Api }) {
  const link = useStore((s) => s.linkStatus)
  const [status, setStatus] = useState<DaemonStatusResult | null>(null)
  const [installed, setInstalled] = useState<DaemonInstallResult | null>(null)
  const [server, setServer] = useState('')
  const [repo, setRepo] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let live = true
    client.localDaemonStatus().then(
      (s) => live && setStatus(s),
      (err: unknown) => live && setError(message(err)),
    )
    return () => {
      live = false
    }
  }, [client])

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

  const unit = installed?.unit_path ?? (status?.installed ? status.unit_path : '')
  return (
    <SettingRow
      label="Sync daemon"
      help={unit
        ? <>Installed at <Code className="break-all">{unit}</Code>.</>
        : 'Keeps the local mirror available for runs that need files on this computer.'}
    >
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      {installed && <CopyableCommand command={installed.note} />}
      {status && !status.installed && !installed && (
        <form
          aria-label="Install sync daemon"
          className="flex min-w-0 flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void install()
          }}
        >
          <div className="grid min-w-0 gap-3 sm:grid-cols-2">
            <FormField label="Server"><Input value={server} onChange={(e) => setServer(e.target.value)} /></FormField>
            <FormField label="Repository"><Input value={repo} onChange={(e) => setRepo(e.target.value)} /></FormField>
          </div>
          <div>
            <Button size="sm" type="submit" disabled={busy || !server || !repo}>{busy ? 'Installing…' : 'Install'}</Button>
          </div>
        </form>
      )}
    </SettingRow>
  )
}

const noRun = 'none'

const ended: Record<string, true> = { merged: true, abandoned: true, failed: true, interrupted: true }

function MirrorRow({ client }: { client: Api }) {
  const runs = useStore((s) => s.runs)
  const live = Object.values(runs).filter((r) => !ended[r.status])
  const [runID, setRunID] = useState('')

  return (
    <SettingRow
      label="Mirror run files"
      labelFor={live.length > 0 ? 'settings-mirror-run' : undefined}
      help="Copies a run's files into your linked repository as the agent works, so you can open them in your editor."
      control={live.length === 0 ? <span className="text-ui-sm text-muted">No active runs</span> : (
        <Select value={runID || noRun} onValueChange={(value) => setRunID(value === noRun ? '' : value)}>
          <SelectTrigger id="settings-mirror-run" className="w-56 max-w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={noRun}>Pick a run</SelectItem>
            {live.map((r) => <SelectItem key={r.id} value={r.id}>{runLabel(r)}</SelectItem>)}
          </SelectContent>
        </Select>
      )}
    >
      {runID && <SyncPanel runID={runID} client={client} />}
    </SettingRow>
  )
}
