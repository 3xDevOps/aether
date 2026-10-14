import { useState } from 'react'
import { AgentSetup } from '@/components/agents/agent-setup'
import { ArrowLeft } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { StatusDot } from '@/components/ui/status-dot'
import { api } from '@/lib/api'
import type { AssistantStatus } from '@/lib/assistant-types'
import { message } from '@/lib/format'
import { useAgentList } from '@/routes/agents/use-agents'
import { useStore } from '@/store'
import { refreshAssistant } from '@/store/assistant-stream'

const examplePrompts = [
  { label: "What's the status of my runs?", text: "What's the status of my runs?" },
  { label: 'Launch a run to …', text: 'Launch a run to ' },
  { label: 'Install Rust in my environment', text: 'Install Rust in my environment' },
  { label: 'How do swarms work?', text: 'How do swarms work?' },
]

export function AssistantSetup({ status }: { status: AssistantStatus }) {
  const { agents, reload } = useAgentList(api, true)
  const [setting, setSetting] = useState('')
  const settingUp = agents?.find((agent) => agent.name === setting)
  if (settingUp) {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-4">
        <div>
          <Button size="sm" variant="ghost" onClick={() => setSetting('')}><ArrowLeft />Back</Button>
        </div>
        <AgentSetup
          key={settingUp.name}
          agent={settingUp}
          client={api}
          onDone={() => {
            reload()
            setSetting('')
            void refreshAssistant(useStore, api)
          }}
        />
      </div>
    )
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4">
      <p className="text-ui">
        The assistant runs on your own agent, in your environment. Set up Claude Code or Codex to start.
      </p>
      <ul aria-label="Agents" className="flex flex-col gap-3">
        {status.agents.map((agent) => (
          <li key={agent.name} className="flex min-w-0 items-start gap-2">
            <AgentGlyph agent={agent.name} colored className="mt-1" />
            <div className="min-w-0 flex-1">
              <p className="flex items-center gap-1.5 font-medium">
                {agent.display_name}
                <StatusDot tone={agent.ready ? 'done' : 'needs-you'} label={agent.ready ? 'Ready' : 'Not ready'} />
              </p>
              <p className="text-ui-sm break-words text-muted">{agent.ready ? 'Ready' : agent.reason}</p>
            </div>
            {!agent.ready && agents?.some((item) => item.name === agent.name) && (
              <Button size="sm" variant="secondary" onClick={() => setSetting(agent.name)}>Set up</Button>
            )}
          </li>
        ))}
      </ul>
      <div>
        <Button size="sm" variant="link" onClick={() => useStore.getState().navigate('agents')}>Open Agents</Button>
      </div>
    </div>
  )
}

export function AssistantRestart() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const restart = async () => {
    setBusy(true)
    setError(undefined)
    try {
      useStore.getState().setAssistantStatus(await api.assistantStart({ restart_environment: true }))
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="flex flex-col gap-3 p-4">
      <Callout
        tone="needs-you"
        title="Restart your environment"
        actions={<Button size="sm" disabled={busy} onClick={() => void restart()}>{busy ? 'Restarting…' : 'Restart environment'}</Button>}
      >
        The assistant needs a newer environment than the one running. Restarting it closes any shells open in it.
      </Callout>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
    </div>
  )
}

export function AssistantFailed({ error }: { error?: string }) {
  const [busy, setBusy] = useState(false)
  const [retryError, setRetryError] = useState<string>()
  const retry = async () => {
    setBusy(true)
    setRetryError(undefined)
    try {
      useStore.getState().setAssistantStatus(await api.assistantStart({}))
    } catch (err) {
      setRetryError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Callout
      tone="failed"
      role="alert"
      title="The assistant stopped"
      className="mx-3 mt-3 shrink-0"
      actions={<Button size="sm" variant="secondary" disabled={busy} onClick={() => void retry()}>{busy ? 'Retrying…' : 'Retry'}</Button>}
    >
      {retryError ?? error ?? 'The agent exited without saying why.'}
    </Callout>
  )
}

export function EmptyConversation({ onPick }: { onPick: (text: string) => void }) {
  return (
    <section aria-label="Suggestions" className="flex min-h-0 flex-1 flex-col justify-center gap-3 overflow-y-auto p-4">
      <p className="text-ui text-muted">Ask in plain language. The assistant acts with your permissions and asks before it stops a run or saves your environment.</p>
      <div className="flex flex-col items-start gap-2">
        {examplePrompts.map(({ label, text }) => (
          <Button key={label} size="sm" variant="secondary" className="h-auto min-h-6 max-w-full py-1 text-left whitespace-normal" onClick={() => onPick(text)}>
            {label}
          </Button>
        ))}
      </div>
    </section>
  )
}
