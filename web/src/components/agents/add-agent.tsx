import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentInfo } from '@/lib/types'

/** Deliberately naive - no quoting or escapes: these are argv templates like `claude {task}`, not shell commands. */
export function splitArgv(template: string): string[] {
  return template.split(' ').filter((w) => w !== '')
}

export function AddAgent({
  client,
  onAdded,
  onCancel,
}: {
  client: Api
  onAdded: (agent: AgentInfo) => void
  onCancel: () => void
}) {
  const [name, setName] = useState('')
  const [standard, setStandard] = useState<string | null>(null)
  const [background, setBackground] = useState<string | null>(null)
  const [enhanced, setEnhanced] = useState('')
  const [mode, setMode] = useState('')
  const [login, setLogin] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const trimmed = name.trim()
  const base = trimmed || 'myagent'
  const standardValue = standard ?? `${base} {task}`
  const tui = splitArgv(standardValue)
  const backgroundValue = background ?? `${tui[0] ?? base} -p {task}`

  const submit = async () => {
    setBusy(true)
    setError(null)
    try {
      const acp = splitArgv(enhanced)
      const credentials = login.trim() ? [login.trim()] : []
      await client.agentRegister({
        name: trimmed,
        executable: tui[0] ?? '',
        tui_args: tui,
        headless_args: splitArgv(backgroundValue),
        ...(acp.length ? { acp_args: acp } : {}),
        ...(mode.trim() ? { acp_mode: mode.trim() } : {}),
        ...(credentials.length ? { credential_paths: credentials } : {}),
      })
      const listed = (await client.agentList()).find((agent) => agent.name === trimmed)
      if (!listed) throw new Error(`agent.list does not list ${trimmed} after agent.register`)
      onAdded(listed)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      aria-label="Add agent"
      className="flex max-w-xl min-w-0 flex-col gap-4"
      onSubmit={(event) => {
        event.preventDefault()
        void submit()
      }}
    >
      <h2 className="text-title text-text">Add agent</h2>
      <p className="text-ui text-muted">Any coding CLI you install in your environment can run as an agent.</p>
      <FormField label="Name" help="How the agent is listed and chosen when a session starts.">
        <Input autoFocus required placeholder="myagent" value={name} onChange={(event) => setName(event.target.value)} />
      </FormField>
      <FormField label="Standard command" help="The first word is the executable, installed in ~/.local/bin. {task} is replaced with the session's task.">
        <Input required value={standardValue} onChange={(event) => setStandard(event.target.value)} />
      </FormField>
      <FormField label="Background command" help="Runs the task once with no terminal.">
        <Input required value={backgroundValue} onChange={(event) => setBackground(event.target.value)} />
      </FormField>
      <FormField label="Enhanced command" help="Optional. The command that serves the Agent Client Protocol on stdio, such as myagent acp.">
        <Input value={enhanced} onChange={(event) => setEnhanced(event.target.value)} />
      </FormField>
      <FormField label="Enhanced start mode" help="Optional. The ACP mode an Enhanced session starts in, such as agent. Pick the one that does not ask before each tool, or agents stall on prompts.">
        <Input value={mode} onChange={(event) => setMode(event.target.value)} />
      </FormField>
      <FormField label="Login file" help="Optional. A file or folder that exists once the agent is signed in, such as ~/.cursor/auth.json. Without one the agent shows as installed, never as logged in.">
        <Input value={login} onChange={(event) => setLogin(event.target.value)} />
      </FormField>
      {error && <Callout tone="failed" role="alert" title="Agent not added">{error}</Callout>}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={busy || !trimmed || tui.length === 0}>
          {busy ? 'Adding…' : 'Add agent'}
        </Button>
        <Button type="button" variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}
