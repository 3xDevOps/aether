import { useEffect } from 'react'
import { useCheckForUpdates, useUpdateNotice, useUpdateTargets } from '@/components/update-banner'
import { checkedTime, packagedLine, verbatim } from '@/components/update-banner-shared'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import { CodeBlock } from '@/components/ui/code'
import { Spinner } from '@/components/ui/spinner'
import { releaseFinishedPlan } from '@/lib/commands'
import { api, type Api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { rootCommand, useSysbox } from '@/lib/use-sysbox'
import { workspaceRuns } from '@/routes/board/selectors'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, useSelf, useStateContext } from '@/store/hooks'

/** The server retries a tailnet dashboard that cannot start, so its error can clear or appear while Settings is open. */
const tailnetDashboardRecheckMs = 30_000

export function ServerSection({ client = api }: { client?: Api } = {}) {
  const info = useStore((s) => s.info)
  const admin = useIsAdmin()
  const update = useUpdateNotice(true)
  const { server, client: cli } = useUpdateTargets()
  const packaged = useStore((s) => s.update?.cli.packaged)
  const openUpdates = useStore((s) => s.setUpdatesOpen)
  const navigate = useStore((s) => s.navigate)
  const clearDismissed = useStore((s) => s.clearDismissedUpdates)
  const checkForUpdates = useCheckForUpdates(client)
  useEffect(() => {
    const read = () =>
      client.serverInfo().then(
        (fresh) => {
          const s = useStore.getState()
          if (s.info && s.info.tailnet_dashboard_error !== fresh.tailnet_dashboard_error) {
            s.setInfo({ ...s.info, tailnet_dashboard_error: fresh.tailnet_dashboard_error })
          }
        },
        () => {},
      )
    void read()
    const timer = setInterval(read, tailnetDashboardRecheckMs)
    return () => clearInterval(timer)
  }, [client])
  if (!info) return null
  const failed = [server, cli].find((t) => t.state === 'check_failed')
  const current = server.state === 'up_to_date' ? server : cli.state === 'up_to_date' ? cli : null
  const checking = server.state === 'checking' || cli.state === 'checking'
  const checkedLine =
    current && !update && checkedTime(current.checkedAt) ? `Up to date. Checked ${checkedTime(current.checkedAt)}.` : ''
  return (
    <SettingsSection title="Server">
      <SettingRow
        label="Version"
        help={[
          `Aether ${info.server_version}, protocol ${info.protocol_version}.`,
          update && `${update.text}.`,
          checking ? 'Checking for updates…' : checkedLine,
          packaged && packagedLine(packaged),
        ].filter(Boolean).join(' ')}
        control={
          <div className="flex flex-wrap gap-1">
            {update?.action && (
              <Button
                size="sm"
                onClick={() => {
                  clearDismissed()
                  openUpdates(true)
                }}
              >
                Update…
              </Button>
            )}
            <Button size="sm" variant="secondary" disabled={checking} onClick={checkForUpdates}>
              {failed ? 'Check now' : 'Check for updates…'}
            </Button>
          </div>
        }
      >
        {failed?.state === 'check_failed' && (
          <div className="space-y-1">
            <p className="text-ui-sm text-muted">
              {failed !== server
                ? 'This computer could not check for a new release:'
                : failed.unread
                  ? "Could not read the server's update status:"
                  : 'The server could not check for a new release:'}
            </p>
            <p className={cn(verbatim, 'text-state-failed')}>{failed.error}</p>
          </div>
        )}
      </SettingRow>
      {info.tailnet_dashboard_error && (
        <SettingRow label="Tailnet dashboard" help="Not serving. The server keeps retrying; restart it after a config change.">
          <p className={cn(verbatim, 'text-state-failed')}>{info.tailnet_dashboard_error}</p>
          <CopyableCommand command="sudo systemctl restart aether-server" />
        </SettingRow>
      )}
      <DockerInRunsRow client={client} />
      <SettingRow
        label="Disk and performance"
        help="CPU, memory, disk and Docker storage, and which runs use them."
        control={<Button size="sm" variant="secondary" onClick={() => navigate('insights', { tab: 'system' })}>Open Insights › System</Button>}
      />
      {admin && <RetainedContainersRow />}
    </SettingsSection>
  )
}

function DockerInRunsRow({ client }: { client: Api }) {
  const { status, error, justInstalled, install } = useSysbox(client)
  if (!status) return null
  const { state, reason, log } = status
  const installing = state === 'installing'
  const command = rootCommand(status)
  const states = {
    on: ['On', 'Agents can run docker, compose and buildx in their runs.'],
    off: ['Off', reason || 'Lets agents build and run containers inside their own runs, isolated from this server. Installs the Sysbox runtime.'],
    unsupported: ['Unavailable on this host', reason],
    installing: ['Installing', undefined],
    failed: ['Install failed', reason],
  } as const
  const [title, detail] = states[state]
  return (
    <SettingRow
      label="Docker inside runs"
      help={(
        <>
          <p className="text-text">{title}</p>
          {detail && <p className="whitespace-pre-wrap break-words">{detail}</p>}
        </>
      )}
      control={((state === 'off' && !command) || state === 'failed') && (
        <Button size="sm" variant="secondary" onClick={install}>
          {state === 'failed' ? 'Try again' : 'Install'}
        </Button>
      )}
    >
      <p role="status" className="flex items-center gap-2 text-ui-sm text-muted empty:hidden">
        {installing && <><Spinner />Installing… this takes a minute or two</>}
        {justInstalled && state === 'on' && 'New runs get it now. Reopen your Environment to use it there.'}
      </p>
      {command && <CopyableCommand command={command} />}
      {error && <p role="alert" className="whitespace-pre-wrap break-words text-ui-sm text-state-failed">{error}</p>}
      {state === 'failed' && log && (
        <details className="min-w-0 text-ui-sm text-muted">
          <summary className="cursor-pointer py-1">Show output</summary>
          <CodeBlock className="max-h-48 overflow-auto whitespace-pre-wrap break-words">{log}</CodeBlock>
        </details>
      )}
    </SettingRow>
  )
}

function RetainedContainersRow() {
  const ctx = useStateContext()
  const caps = useCapability()
  const self = useSelf()
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const workspaceName = useStore((s) => s.workspaces[s.activeWorkspace]?.name)
  const openDialog = useStore((s) => s.openPaletteDialog)
  if (!caps.hasMethod('run.release')) return null
  const count = releaseFinishedPlan(workspaceRuns(activeWorkspace, ctx), caps, self).eligible.length
  const where = workspaceName ? ` in ${workspaceName}` : ''
  return (
    <SettingRow
      label="Retained containers"
      help={count > 0
        ? `${count} finished ${count === 1 ? 'run keeps its' : 'runs keep their'} container${where}. Removing containers prevents reopening those runs; it does not delete all run data, homes, repositories or history.`
        : `No finished run keeps a container${where}.`}
      control={(
        <Button size="sm" variant="secondary" disabled={count === 0} onClick={() => openDialog('release-finished')}>
          Free retained containers…
        </Button>
      )}
    />
  )
}
