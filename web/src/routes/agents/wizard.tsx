// `agent add` as a wizard: name and argv templates, then the agent-setup
// shell embedded right here (registration happens when that shell exits
// cleanly, so the wizard never navigates away from it), then the result.

import { useState } from 'react'
import { Button } from '@/components/ui/button'
import type { AgentInfo } from '@/lib/types'
import { ShellPane } from '@/routes/shell/pane'
import { useStore } from '@/store'

const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

/**
 * An argv template split on single spaces. Deliberately naive - no quoting,
 * no escapes - because these are argv templates like `claude {task}`, not
 * shell commands; a name with a space in it belongs to the CLI's flag form.
 */
export function splitArgv(template: string): string[] {
  return template.split(' ').filter((w) => w !== '')
}

type Step = 'form' | 'shell' | 'done'

export function AgentWizard({
  agents,
  onRegistered,
  onCancel,
}: {
  /** The current list, for shipped-name detection and duplicate hints. */
  agents: AgentInfo[]
  /** A clean shell exit registered the agent; the caller refetches. */
  onRegistered: () => void
  onCancel: () => void
}) {
  const workspaces = useStore((s) => s.workspaces)
  const activeWorkspace = useStore((s) => s.activeWorkspace)
  const openShell = useStore((s) => s.openShell)
  const shellRequest = useStore((s) => s.shellRequest)

  const [step, setStep] = useState<Step>('form')
  const [name, setName] = useState('')
  // Empty until the member picks one, so the sidebar's choice keeps
  // following them until they mean to set up an agent somewhere else.
  const [picked, setPicked] = useState('')
  // Argv templates follow the name until the user edits them.
  const [tui, setTui] = useState<string | null>(null)
  const [headless, setHeadless] = useState<string | null>(null)

  const choices = Object.values(workspaces)
  const workspaceID = picked || activeWorkspace || choices[0]?.id || ''

  const trimmed = name.trim()
  const shipped = agents.some((a) => a.name === trimmed && a.source === 'shipped')
  // The CLI's argv template defaults (cmd/aether/agent.go): `{task}` is the
  // placeholder the server substitutes at launch.
  const base = trimmed || 'agent'
  const tuiValue = tui ?? `${base} {task}`
  const headlessValue = headless ?? `${base} -p {task}`

  const start = () => {
    if (!trimmed || !workspaceID) return
    // A refusal (unknown workspace, name collision) comes back on the
    // socket's ack and renders verbatim in the pane; nothing is predicted
    // here.
    openShell({
      workspace: { id: workspaceID },
      mode: 'agent-setup',
      harness: trimmed,
      // Shipped agents carry their own argv templates server-side; only a
      // member-defined name proposes them.
      ...(shipped
        ? {}
        : {
            tui_args: splitArgv(tuiValue),
            headless_args: splitArgv(headlessValue),
          }),
    })
    setStep('shell')
  }

  if (step === 'done') {
    return (
      <div className="space-y-3 rounded-md border p-4">
        <p className="text-sm font-medium">Agent registered</p>
        <p className="text-sm text-muted-foreground">
          {trimmed} is set up: the login is persisted and the workspace tools
          are snapshotted.
        </p>
        <Button size="sm" onClick={onCancel}>
          Close
        </Button>
      </div>
    )
  }

  if (step === 'shell' && shellRequest) {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-2">
        <p className="px-1 text-xs text-muted-foreground">
          Install and log in to {trimmed} below. A dirty exit offers resume
          and reset right in the pane.
        </p>
        <div className="min-h-0 flex-1">
          <ShellPane
            req={shellRequest}
            onExit={(clean) => {
              if (!clean) return
              onRegistered()
              setStep('done')
            }}
          />
        </div>
      </div>
    )
  }

  return (
    <form
      className="max-w-md space-y-3 rounded-md border p-4"
      onSubmit={(e) => {
        e.preventDefault()
        start()
      }}
    >
      <p className="text-sm font-medium">Add an agent</p>
      <label className="block space-y-1 text-sm">
        Name
        <input
          autoFocus
          className={field}
          placeholder="claude"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <label className="block space-y-1 text-sm">
        Workspace
        <select
          className={field}
          value={workspaceID}
          onChange={(e) => setPicked(e.target.value)}
        >
          {choices.map((w) => (
            <option key={w.id} value={w.id}>
              {w.name}
            </option>
          ))}
        </select>
      </label>
      {!shipped && (
        <>
          <label className="block space-y-1 text-sm">
            TUI command
            <input
              className={field}
              value={tuiValue}
              onChange={(e) => setTui(e.target.value)}
            />
          </label>
          <label className="block space-y-1 text-sm">
            Headless command
            <input
              className={field}
              value={headlessValue}
              onChange={(e) => setHeadless(e.target.value)}
            />
          </label>
          <p className="text-xs text-muted-foreground">
            {'{task}'} is replaced with the run's task at launch.
          </p>
        </>
      )}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={!trimmed || !workspaceID}>
          Open setup shell
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}
