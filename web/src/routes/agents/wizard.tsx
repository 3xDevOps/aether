// `agent add` as a wizard: collect the name and launch templates, then give
// concise instructions for setup in the member's persistent environment home.
// Confirming the install also saves the environment, because an executable
// that only exists in the running container is not in the image runs start
// from.

import { useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api, type Api } from '@/lib/api'
import { TerminalDock } from '@/routes/board/terminal-dock'
import type { AgentInfo } from '@/lib/types'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

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
      <div className="min-w-0 space-y-4 border-y border-state-done/30 bg-state-done/5 px-4 py-4 sm:px-5">
        <div className="flex items-center gap-2">
          <span className="text-xs font-semibold text-state-done" aria-hidden="true">
            3
          </span>
          <p className="text-base font-semibold">
            {shipped ? 'Agent installed' : 'Agent registered'}
          </p>
        </div>
        <p className="text-sm leading-6 text-muted-foreground">
          {trimmed} is available in the run launcher. Its executable and user-local files
          persist in your member home, and your environment is saved as{' '}
          <span className="break-all font-mono">{saved}</span>, so new runs start from it.
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
            ? 'min-w-0 space-y-4 border-y bg-sidebar px-4 py-4 sm:px-5'
            : 'min-w-0 max-w-2xl space-y-4 border-y bg-sidebar px-4 py-4 sm:px-5'
        }
      >
        <div className="flex items-center gap-2">
          <span className="text-xs font-semibold text-primary" aria-hidden="true">
            2
          </span>
          <p className="text-base font-semibold">Set up {trimmed}</p>
        </div>
        {hasTerminal ? (
          <>
            <p className="text-sm leading-6 text-muted-foreground">
              The install command is ready in your environment terminal:
            </p>
            <TerminalDock client={client} openOnMount initialLine={installScript} />
            <p className="text-sm leading-6 text-muted-foreground">
              Complete the vendor login in that terminal, then return here.
            </p>
            <code className="block min-w-0 overflow-x-auto rounded-[2px] border bg-muted px-3 py-2 font-mono text-xs">
              {installScript}
            </code>
          </>
        ) : (
          <>
            <p className="text-sm leading-6 text-muted-foreground">
              Open your environment terminal and run the install command there:
            </p>
            <code className="block min-w-0 overflow-x-auto rounded-[2px] border bg-muted px-3 py-2 font-mono text-xs">
              aether terminal
            </code>
            <pre className="min-w-0 overflow-x-auto rounded-[2px] border bg-muted p-3 font-mono text-xs">
              {installScript}
            </pre>
            <p className="text-sm leading-6 text-muted-foreground">
              Complete the vendor login in that terminal, then return here.
            </p>
          </>
        )}
        {error && (
          <p className="border-y border-state-failed/30 bg-state-failed/5 px-3 py-2 text-[13px] text-state-failed">
            {error}
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button type="button" size="sm" onClick={() => void finish()} disabled={busy}>
            {phase === 'checking'
              ? 'Checking installation...'
              : phase === 'saving'
                ? 'Saving environment...'
                : "I've installed and logged in"}
          </Button>
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
      className="min-w-0 max-w-2xl space-y-4 border-y bg-sidebar px-4 py-4 sm:px-5"
      onSubmit={(e) => {
        e.preventDefault()
        start()
      }}
    >
      <div className="flex items-center gap-2">
        <span className="text-xs font-semibold text-primary" aria-hidden="true">
          1
        </span>
        <p className="text-base font-semibold">Add an agent</p>
      </div>
      <Label className="block min-w-0 max-w-md space-y-1">
        Name
        <Input
          autoFocus
          placeholder="claude"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </Label>
      {!shipped && (
        <div className="space-y-4">
          <Label className="block min-w-0 space-y-1">
            TUI command
            <Input
              value={tuiValue}
              onChange={(e) => setTui(e.target.value)}
            />
          </Label>
          <Label className="block min-w-0 space-y-1">
            Headless command
            <Input
              value={headlessValue}
              onChange={(e) => setHeadless(e.target.value)}
            />
          </Label>
          <p className="text-[13px] leading-5 text-muted-foreground">
            {'{task}'} is replaced with the run's task at launch.
          </p>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
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
