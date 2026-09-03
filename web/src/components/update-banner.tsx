// The two update prompts, above everything the shell renders. Both are
// answers to one `update.check` call: the CLI on this machine is behind, or
// the server it talks to is. Only the first one can be acted on from here -
// the dashboard has no way to write to the server host - so the second says
// exactly what to run there instead of offering a button that cannot work.

import { X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { CopyableCommand } from '@/components/copyable-command'
import { desktopBridge } from '@/components/shell/title-bar'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { UpdateStatus } from '@/lib/types'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import type { UpdateKind } from '@/store/ui'

const banner =
  'flex flex-wrap items-start gap-x-3 gap-y-1 border-b bg-card px-3 py-2 text-sm'

/** Release tags arrive as "v1.2.3", the shell's version as "1.2.3". */
function bare(version: string): string {
  return version.replace(/^v/, '')
}

/**
 * Whether the desktop app was built by a different CLI than the one serving
 * this gateway. Both sides have to be known: a browser tab has no shell, and
 * a dev-built shell reports no version, and neither is a mismatch.
 */
function shellIsStale(cliVersion: string | undefined): boolean {
  const shell = desktopBridge()?.shellVersion
  if (!shell || !cliVersion) return false
  return bare(shell) !== bare(cliVersion)
}

/** The dismiss control both banners carry. */
function Dismiss({ kind, version }: { kind: UpdateKind; version: string }) {
  const dismiss = useStore((s) => s.dismissUpdate)
  return (
    <Button
      variant="ghost"
      size="icon"
      className="ml-auto size-6"
      aria-label="Dismiss"
      onClick={() => dismiss(kind, version)}
    >
      <X className="size-3.5" aria-hidden />
    </Button>
  )
}

/**
 * Both banners, and the one `update.check` they read. Renders nothing where
 * the gateway does not serve the verb: only the desktop gateway can update
 * the binary beside it, so a remote monitor has nothing to say here.
 */
export function UpdateBanners({ client = api }: { client?: Api } = {}) {
  const serves = useCapability().hasLocal('update.check')
  const update = useStore((s) => s.update)
  const setUpdate = useStore((s) => s.setUpdate)

  useEffect(() => {
    if (!serves) return
    let live = true
    void client
      .localUpdateCheck()
      .then((status) => {
        if (live) setUpdate(status)
      })
      // A failed check is not worth a banner of its own: the release lookup
      // is a network read the member did not ask for.
      .catch(() => {})
    return () => {
      live = false
    }
  }, [serves, client, setUpdate])

  if (!serves || !update) return null
  return (
    <>
      <CliBanner update={update} client={client} />
      <ServerBanner update={update} />
    </>
  )
}

type ApplyState =
  | { name: 'idle' }
  | { name: 'applying' }
  | { name: 'done'; version: string; restarting: boolean; note?: string }
  | { name: 'failed'; detail: string }

/**
 * The CLI is behind. Every member sees this: the binary is on their own
 * machine, so no role gates it.
 */
function CliBanner({ update, client }: { update: UpdateStatus; client: Api }) {
  const latest = update.cli.latest ?? ''
  const dismissed = useStore((s) => s.dismissedUpdates.cli)
  const cliVersion = useStore((s) => s.capabilities?.version)
  const [apply, setApply] = useState<ApplyState>({ name: 'idle' })

  if (!update.cli.update_available || !latest || dismissed === latest) return null

  const run = async () => {
    setApply({ name: 'applying' })
    try {
      const result = await client.localUpdateApply()
      setApply({
        name: 'done',
        version: result.version,
        restarting: result.restarting,
        note: result.note,
      })
    } catch (err) {
      // The gateway's own message, verbatim: it names the directory and the
      // exact sudo command when the binary is not writable.
      setApply({ name: 'failed', detail: message(err) })
    }
  }

  return (
    <div role="status" className={banner}>
      <div className="min-w-0 space-y-1">
        <p>
          <span className="font-medium">Aether {latest} is available.</span> You
          are running {update.cli.version}.
        </p>
        {update.cli.can_self_update ? (
          <p className="text-muted-foreground">
            Updating replaces the aether binary on this machine and restarts the
            dashboard.
          </p>
        ) : (
          <p className="text-muted-foreground">
            Self-update is not supported on Windows. Download {latest} from the
            release page and replace the binary yourself.
          </p>
        )}
        {shellIsStale(cliVersion) && (
          <p className="text-muted-foreground">
            Rebuild the desktop app with <code className="font-mono">aether gui build</code>{' '}
            after updating.
          </p>
        )}
        {apply.name === 'done' &&
          (apply.restarting ? (
            <p className="text-muted-foreground">
              Updated to {apply.version}. Restarting the dashboard.
            </p>
          ) : (
            <p className="text-muted-foreground">
              Updated to {apply.version}.{apply.note ? ` ${apply.note}` : ''}
            </p>
          ))}
        {apply.name === 'failed' && (
          <p className="font-mono text-xs text-state-failed">{apply.detail}</p>
        )}
      </div>
      <div className="flex items-center gap-2">
        {update.cli.can_self_update && apply.name !== 'done' && (
          <Button
            size="sm"
            disabled={apply.name === 'applying'}
            onClick={() => void run()}
          >
            {apply.name === 'applying' ? 'Updating...' : 'Update now'}
          </Button>
        )}
        {update.cli.release_url && (
          <a
            href={update.cli.release_url}
            target="_blank"
            rel="noreferrer"
            className="text-xs underline underline-offset-2 hover:text-foreground"
          >
            Release notes
          </a>
        )}
      </div>
      <Dismiss kind="cli" version={latest} />
    </div>
  )
}

/**
 * The server is behind. Admins only: capability says the gateway can carry
 * the check, the role says whether this member is the one who runs commands
 * on the server host. The dashboard cannot update the server at all - the
 * binary is on another machine, behind SSH - so this names the two commands
 * rather than pretending to a button.
 */
function ServerBanner({ update }: { update: UpdateStatus }) {
  const latest = update.cli.latest ?? ''
  const isAdmin = useIsAdmin()
  const dismissed = useStore((s) => s.dismissedUpdates.server)

  if (!update.server_behind || !latest) return null
  if (!isAdmin) return null
  if (dismissed === latest) return null

  return (
    <div role="status" className={banner}>
      <div className="min-w-0 space-y-1">
        <p>
          <span className="font-medium">The server is behind.</span> Server{' '}
          {update.server_version}, latest {latest}.
        </p>
        <p className="text-muted-foreground">
          The dashboard cannot update the server. Run these on the server host:
        </p>
        <div className="space-y-1">
          <CopyableCommand command="sudo aether update" />
          <CopyableCommand command="sudo systemctl restart aether-server" />
        </div>
      </div>
      <Dismiss kind="server" version={latest} />
    </div>
  )
}
