import { useState } from 'react'
import { AssistantSignIn } from '@/components/assistant/sign-in'
import { ArrowLeft } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { StatusDot } from '@/components/ui/status-dot'
import { api } from '@/lib/api'
import type { AssistantStatus } from '@/lib/assistant-types'
import { assistantAsk } from '@/lib/commands'
import { message } from '@/lib/format'
import { useOpenPullRequest } from '@/routes/diff/publish-pr'
import { useStore } from '@/store'
import { refreshAssistant } from '@/store/assistant-stream'
import type { AttachedTarget } from '@/store/view-context'

interface Example {
  label: string
  text: string
  /** What the prompt is about, sent even with page sharing off. */
  attachments?: AttachedTarget[]
}

const asked = (text: string, attachments?: AttachedTarget[]): Example => ({ label: text, text, attachments })

const generalPrompts = [
  asked("What's the status of my runs?"),
  { label: 'Launch a run to …', text: 'Launch a run to ' },
  asked('Install Rust in my environment'),
]

/** The page's own prompts, each attached to what it is about: a run's follow its state, and nothing offers to merge a pull request the run does not have. */
function usePagePrompts(): Example[] {
  const route = useStore((s) => s.route)
  const runID = route.name === 'run' ? route.params.runId : undefined
  const run = useStore((s) => (runID ? s.runs[runID] : undefined))
  const pr = useOpenPullRequest(runID)
  if (run) {
    const target: AttachedTarget = { kind: 'run', workspace_id: run.workspace_id, run_id: run.id, label: run.id }
    const ask = assistantAsk(run)
    return [
      ...(ask ? [asked(ask.draft, [target])] : []),
      ...(pr ? [asked('Review and merge when checks pass', [{ kind: 'pr', workspace_id: run.workspace_id, run_id: run.id, pr: pr.number, repository: pr.repository, label: `PR #${pr.number}` }])] : []),
    ]
  }
  return route.name === 'board' ? [asked('What needs me?')] : []
}

export function AssistantSetup({ status }: { status: AssistantStatus }) {
  const [signing, setSigning] = useState('')
  const signingIn = status.agents.find((agent) => agent.name === signing)
  if (signingIn) {
    return (
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-4">
        <div>
          <Button size="sm" variant="ghost" onClick={() => setSigning('')}><ArrowLeft />Back</Button>
        </div>
        <AssistantSignIn
          key={signingIn.name}
          agent={signingIn}
          client={api}
          onDone={() => {
            setSigning('')
            void refreshAssistant(useStore, api)
          }}
        />
      </div>
    )
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4">
      <p className="text-ui">
        The assistant runs Claude Code or Codex in a container of its own, with a sign-in of its own. Sign in to one to start.
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
            {!agent.ready && (
              <Button size="sm" variant="secondary" onClick={() => setSigning(agent.name)}>Sign in</Button>
            )}
          </li>
        ))}
      </ul>
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

export function EmptyConversation({ autoApprove, onPick }: { autoApprove: boolean; onPick: (text: string) => void }) {
  const examples = [...usePagePrompts(), ...generalPrompts].slice(0, 4)
  return (
    <section aria-label="Suggestions" className="flex min-h-0 flex-1 flex-col justify-center gap-3 overflow-y-auto p-4">
      <p className="text-ui text-muted">
        Ask in plain language. The assistant acts with your permissions and {autoApprove ? 'launches, messages and stops your runs and saves your environment without asking.' : 'asks before it launches, messages or stops a run, or saves your environment.'}
      </p>
      <div className="flex flex-col items-start gap-2">
        {examples.map(({ label, text, attachments }) => (
          <Button
            key={label}
            size="sm"
            variant="secondary"
            className="h-auto min-h-6 max-w-full py-1 text-left whitespace-normal"
            onClick={() => {
              onPick(text)
              if (attachments) useStore.getState().askAssistant({ draft: text, attachments })
            }}
          >
            {label}
          </Button>
        ))}
      </div>
    </section>
  )
}
