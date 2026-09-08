// `agent add` as a wizard: collect the name and launch templates, then give
// concise instructions for setup in the member's persistent environment home.
// Confirming the install also saves the environment, because an executable
// that only exists in the running container is not in the image runs start
// from.

import { useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { api, type Api } from '@/lib/api'
import { TerminalDock } from '@/routes/board/terminal-dock'
import type { AgentInfo } from '@/lib/types'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

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

/** What the confirm button is doing, and what it says while it does it. */
type Phase = 'idle' | 'checking' | 'saving'

export function AgentWizard({
  agents,
  harness,
  onRegistered,
  onCancel,
  client = api,
}: {
  /** The current list, for shipped-name detection and installer details. */
  agents: AgentInfo[]
  /** The harness to set up, when the caller already knows it (onboarding).
   * The form is skipped and the setup instructions show straight away. */
  harness?: string
  /** Installation was verified; the caller refetches. */
  onRegistered: () => void
  onCancel: () => void
  /** API client used by an embedded wizard or test fixture. */
  client?: Api
}) {
  const [step, setStep] = useState<Step>(harness ? 'instructions' : 'form')
  const [name, setName] = useState(harness ?? '')
  // Argv templates follow the name until the user edits them.
  const [tui, setTui] = useState<string | null>(null)
  const [headless, setHeadless] = useState<string | null>(null)
  const [phase, setPhase] = useState<Phase>('idle')
  const [saved, setSaved] = useState('')
  const [error, setError] = useState<string | null>(null)
  const caps = useCapability()
  const setEnvStatus = useStore((s) => s.setEnvTerminalStatus)
  const busy = phase !== 'idle'

  const trimmed = name.trim()
  const selected = agents.find((a) => a.name === trimmed)
  const shipped = selected?.source === 'shipped'
  const installScript =
    selected?.install_script || `install ${trimmed || 'the agent'} into ~/.local/bin`
  // The CLI's argv template defaults: `{task}` is the placeholder the server
  // substitutes at launch.
  const hasTerminal = caps.hasWS('terminal')
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
    setPhase('checking')
    setError(null)
    try {
      if (!shipped) {
        await client.agentRegister({
          name: trimmed,
          executable: trimmed,
          tui_args: splitArgv(tuiValue),
          headless_args: splitArgv(headlessValue),
        })
      }
      const listed = await client.agentList()
      if (!listed.some((agent) => agent.name === trimmed && agent.installed === true)) {
        throw new Error(
          `agent.list: ${trimmed} is not detected as installed in your account's ~/.local/bin. Finish the installation in the environment terminal, then try again.`,
        )
      }
      setPhase('saving')
      const image = (await client.envSave()).image
      // The dock shares this status, so its unsaved hint and its saved image
      // follow the save whoever ran it.
      const status = useStore.getState().envTerminal.status
      setEnvStatus({ ...(status ?? { running: true, tabs: [] }), saved_image: image })
      setSaved(image)
      onRegistered()
      setStep('done')
    } catch (err) {
      setError(message(err))
    } finally {
      setPhase('idle')
    }
  }

  if (step === 'done') {
    return (
      <div className="space-y-3 rounded-md border p-4">
        <p className="text-sm font-medium">
          {shipped ? 'Agent installed' : 'Agent registered'}
        </p>
        <p className="text-sm text-muted-foreground">
          {trimmed} is available in the run launcher. Its executable and user-local files
          persist in your member home, and your environment is saved as{' '}
          <span className="font-mono">{saved}</span>, so new runs start from it.
          Vendor login is checked by the agent when it starts, not here.
        </p>
        <Button size="sm" onClick={onCancel}>
          Close
        </Button>
      </div>
    )
  }

  if (step === 'instructions') {
    return (
      <div
        className={
          hasTerminal
            ? 'space-y-3 rounded-md border p-4'
            : 'max-w-md space-y-3 rounded-md border p-4'
        }
      >
        <p className="text-sm font-medium">Set up {trimmed}</p>
        {hasTerminal ? (
          <>
            <p className="text-sm text-muted-foreground">
              The install command is ready in your environment terminal:
            </p>
            <TerminalDock client={client} openOnMount initialLine={installScript} />
            <p className="text-sm text-muted-foreground">
              Complete the vendor login in that terminal, then return here.
            </p>
            <code className="block rounded-md bg-muted px-2 py-1 font-mono text-xs">
              {installScript}
            </code>
          </>
        ) : (
          <>
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
          </>
        )}
        {error && <p className="text-xs text-state-failed">{error}</p>}
        <div className="flex gap-2">
          <Button type="button" size="sm" onClick={() => void finish()} disabled={busy}>
            {phase === 'checking'
              ? 'Checking installation...'
              : phase === 'saving'
                ? 'Saving environment...'
                : "I've installed and logged in"}
          </Button>
          {/* Embedded with a harness there is no form to go back to, and
              the host wizard carries the only Back. */}
          {!harness && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => setStep('form')}
              disabled={busy}
            >
              Back
            </Button>
          )}
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
