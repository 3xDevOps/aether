// `agent add` as a wizard: collect the name and launch templates, then give
// concise instructions for setup in the member's persistent environment home.

import { useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import type { AgentInfo } from '@/lib/types'

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

type Step = 'form' | 'instructions' | 'done'

export function AgentWizard({
  agents,
  harness,
  onRegistered,
  onCancel,
}: {
  /** The current list, for shipped-name detection and installer details. */
  agents: AgentInfo[]
  /** The harness to set up, when the caller already knows it (onboarding).
   * The form is skipped and the setup instructions show straight away. */
  harness?: string
  /** The setup confirmation registered the agent; the caller refetches. */
  onRegistered: () => void
  onCancel: () => void
}) {
  const [step, setStep] = useState<Step>(harness ? 'instructions' : 'form')
  const [name, setName] = useState(harness ?? '')
  // Argv templates follow the name until the user edits them.
  const [tui, setTui] = useState<string | null>(null)
  const [headless, setHeadless] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const trimmed = name.trim()
  const selected = agents.find((a) => a.name === trimmed)
  const shipped = selected?.source === 'shipped'
  const installScript =
    selected?.install_script || `install ${trimmed || 'the agent'} into ~/.local/bin`
  // The CLI's argv template defaults: `{task}` is the placeholder the server
  // substitutes at launch.
  const base = trimmed || 'agent'
  const tuiValue = tui ?? `${base} {task}`
  const headlessValue = headless ?? `${base} -p {task}`

  const start = () => {
    if (!trimmed) return
    setError(null)
    setStep('instructions')
  }

  const finish = async () => {
    if (!trimmed || busy) return
    setBusy(true)
    setError(null)
    try {
      if (!shipped) {
        await api.agentRegister({
          name: trimmed,
          executable: trimmed,
          tui_args: splitArgv(tuiValue),
          headless_args: splitArgv(headlessValue),
        })
      }
      onRegistered()
      setStep('done')
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  if (step === 'done') {
    return (
      <div className="space-y-3 rounded-md border p-4">
        <p className="text-sm font-medium">Agent registered</p>
        <p className="text-sm text-muted-foreground">
          {trimmed} is ready. Its login and user-local files persist in your member home.
        </p>
        <Button size="sm" onClick={onCancel}>
          Close
        </Button>
      </div>
    )
  }

  if (step === 'instructions') {
    return (
      <div className="max-w-md space-y-3 rounded-md border p-4">
        <p className="text-sm font-medium">Set up {trimmed}</p>
        <p className="text-sm text-muted-foreground">
          Open your environment terminal and run the install command there:
        </p>
        <code className="block rounded-md bg-muted px-2 py-1 font-mono text-xs">
          aether terminal
        </code>
        <pre className="overflow-x-auto rounded-md bg-muted p-2 font-mono text-xs">
          {installScript}
        </pre>
        <p className="text-sm text-muted-foreground">
          Complete the vendor login in that terminal, then return here.
        </p>
        {error && <p className="text-xs text-state-failed">{error}</p>}
        <div className="flex gap-2">
          <Button type="button" size="sm" onClick={() => void finish()} disabled={busy}>
            {busy ? 'Registering...' : "I've installed and logged in"}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => (harness ? onCancel() : setStep('form'))}
            disabled={busy}
          >
            Back
          </Button>
        </div>
        {/* Step 2 replaces these instructions with the terminal dock. */}
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
        <Button type="submit" size="sm" disabled={!trimmed}>
          Continue
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}
