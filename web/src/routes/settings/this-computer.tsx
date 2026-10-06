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
  const [changing, setChanging] = useState(false)
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
  const offered = status !== null && !status.installed && !installed
  const editable = changing || !link?.linked
  return (
    <SettingRow
      label="Sync daemon"
      help="Fetches each run's branch into your clone as the agent commits, so the work is there for git without a manual fetch."
      control={offered && (
        <Button size="sm" variant="secondary" type="submit" form="daemon-install" disabled={busy || !server || !repo}>
          {busy ? 'Installing…' : 'Install'}
        </Button>
      )}
    >
      {unit && <p className="text-ui-sm text-muted">Installed at <Code className="break-all">{unit}</Code>.</p>}
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      {installed && <CopyableCommand command={installed.note} />}
      {offered && (
        <form
          id="daemon-install"
          aria-label="Install sync daemon"
          className="flex min-w-0 flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void install()
          }}
        >
          {editable ? (
            <div className="grid min-w-0 gap-3 sm:grid-cols-2">
              <FormField label="Server"><Input value={server} onChange={(e) => setServer(e.target.value)} /></FormField>
              <FormField label="Repository"><Input value={repo} onChange={(e) => setRepo(e.target.value)} /></FormField>
            </div>
          ) : (
            <p className="min-w-0 text-ui-sm text-muted">
              For <Code className="break-all">{repo}</Code> on <Code>{server}</Code>{' '}
              <Button type="button" variant="link" size="sm" onClick={() => setChanging(true)}>Change</Button>
            </p>
          )}
        </form>
      )}
    </SettingRow>
  )
}

const ended: Record<string, true> = { merged: true, abandoned: true, failed: true, interrupted: true }

function MirrorRow({ client }: { client: Api }) {
  const runs = useStore((s) => s.runs)
  const sessions = useStore((s) => s.syncSessions)
  const live = Object.values(runs)
    .filter((r) => !ended[r.status])
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
  const [chosen, setChosen] = useState('')
  const [changing, setChanging] = useState(false)
  const runID = live.some((r) => r.id === chosen)
    ? chosen
    : (live.find((r) => sessions[r.id]) ?? live[0])?.id ?? ''
  const shown = live.find((r) => r.id === runID)

  return (
    <SettingRow
      label="Mirror run files"
      labelFor={changing ? 'settings-mirror-run' : undefined}
      help="Copies a run's files into your linked repository as the agent works, so you can open them in your editor."
      control={!shown && <span className="text-ui-sm text-muted">No active runs</span>}
    >
      {shown && (
        changing ? (
          <Select
            defaultOpen
            value={runID}
            onValueChange={(value) => {
              setChosen(value)
              setChanging(false)
            }}
          >
            <SelectTrigger id="settings-mirror-run" className="w-72 max-w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {live.map((r) => <SelectItem key={r.id} value={r.id}>{runLabel(r)}</SelectItem>)}
            </SelectContent>
          </Select>
        ) : (
          <p className="flex min-w-0 items-center gap-1 text-ui-sm text-muted">
            <span className="shrink-0">Run</span>
            <span className="min-w-0 truncate text-text">{runLabel(shown)}</span>
            {live.length > 1 && (
              <Button type="button" variant="link" size="sm" className="shrink-0" onClick={() => setChanging(true)}>Change</Button>
            )}
          </p>
        )
      )}
      {shown && <SyncPanel key={runID} runID={runID} client={client} />}
    </SettingRow>
  )
}
