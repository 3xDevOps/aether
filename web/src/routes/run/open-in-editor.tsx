import { useRef, useState } from 'react'
import type * as React from 'react'
import { CopyableCommand } from '@/components/copyable-command'
import { ExternalLink } from '@/components/icons'
import type { ExtraItem } from '@/components/run-actions'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { phoneScreen, useMediaQuery } from '@/lib/hooks'
import { allowed } from '@/lib/permissions'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

/** A run id as a host name carries it: one lowercase DNS label, the grammar internal/cli accepts. */
const runHostLabel = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/

/**
 * The ssh host `aether ssh-config` gives every run; internal/cli names the
 * suffix. Null for an id that is not a host label: it would be read as syntax
 * by an editor link or a shell.
 */
export function runHost(runID: string): string | null {
  return runHostLabel.test(runID) ? `${runID}.aether` : null
}

/** A run's checkout inside its container. */
const checkout = '/workspace'

export const editors: { name: string; link: (host: string) => string }[] = [
  { name: 'VS Code', link: (host) => `vscode://vscode-remote/ssh-remote+${host}${checkout}` },
  { name: 'Cursor', link: (host) => `cursor://vscode-remote/ssh-remote+${host}${checkout}` },
  { name: 'Zed', link: (host) => `zed://ssh/${host}${checkout}` },
]

/**
 * The More menu's Open in editor item and its dialog, for a live run this
 * member may control. A phone has no editor to hand the run to, so it gets
 * neither.
 */
export function useOpenInEditor(run: RunRecord): { item: ExtraItem | null; dialog: React.ReactNode } {
  const phone = useMediaQuery(phoneScreen)
  const self = useSelf()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const paused = useStore((s) => s.pausedRuns[run.id])
  const [open, setOpen] = useState(false)
  const returnTo = useRef<HTMLElement | null>(null)

  const live = run.status === 'running' || run.status === 'needs-attention'
  const host = runHost(run.id)
  if (phone || !live || !host || !allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })) {
    return { item: null, dialog: null }
  }
  const item: ExtraItem = {
    id: 'open-in-editor',
    label: 'Open in editor…',
    Icon: ExternalLink,
    ...(paused ? { disabled: true, description: 'Resume the session first' } : {}),
    onSelect: (trigger) => {
      returnTo.current = trigger
      setOpen(true)
    },
  }
  const dialog = open && (
    <Dialog open onOpenChange={setOpen}>
      <DialogContent
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          returnTo.current?.focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>Open in editor</DialogTitle>
          <DialogDescription>Your editor connects to this session&apos;s container over SSH and opens its checkout.</DialogDescription>
        </DialogHeader>
        <div className="flex flex-wrap gap-2">
          {editors.map((editor) => (
            <Button key={editor.name} variant="secondary" onClick={() => window.location.assign(editor.link(host))}>
              {editor.name}
            </Button>
          ))}
        </div>
        <div className="grid min-w-0 gap-1">
          <p className="text-ui-sm text-muted">Or from a terminal</p>
          <CopyableCommand command={`ssh ${host}`} />
        </div>
        <div className="grid min-w-0 gap-1">
          <p className="text-ui-sm text-muted">First time on this computer? Run this once, so ssh and your editor know the host</p>
          <CopyableCommand command="aether ssh-config" />
        </div>
      </DialogContent>
    </Dialog>
  )
  return { item, dialog }
}
