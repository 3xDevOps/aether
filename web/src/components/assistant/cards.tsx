import { useState } from 'react'
import { isLaunch, launchTitle, useLaunchRows } from '@/components/assistant/launch-summary'
import { AssistantSettingsLink } from '@/components/assistant/suggestion'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Code } from '@/components/ui/code'
import { RelativeTime } from '@/components/ui/relative-time'
import { IconTile } from '@/components/ui/icon-tile'
import { RequestCard, RequestTimeBar } from '@/components/ui/request-card'
import { RequestAction, RequestActions, RequestFooter } from '@/components/ui/request-choice'
import { RequestRows, RequestSection } from '@/components/ui/request-section'
import { api } from '@/lib/api'
import type { AssistantConfirmation } from '@/lib/assistant-types'
import { message } from '@/lib/format'
import { useSecondsUntil } from '@/lib/hooks'
import type { SessionRequest } from '@/lib/session-types'
import { AcpRequestCard } from '@/routes/run/session-requests'
import { useStore } from '@/store'
import { assistantSessionID } from '@/store/assistant'

function useAction() {
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string>()
  const run = async (key: string, work: () => Promise<unknown>) => {
    if (busy) return
    setBusy(key)
    setError(undefined)
    try {
      await work()
      // Stays busy: the card leaves when the server's status drops it, and a second answer would be refused.
    } catch (err) {
      setError(message(err))
      setBusy(null)
    }
  }
  return { busy, error, run }
}

function useAssistantAgent(): { harness: string; name: string } {
  const harness = useStore((s) => s.assistantStatus?.agent ?? '')
  const name = useStore((s) => s.assistantStatus?.agents.find((agent) => agent.name === harness)?.display_name) ?? harness
  return { harness, name: name || 'The assistant' }
}

export function AssistantRequestCard({ request }: { request: SessionRequest }) {
  const { busy, error, run } = useAction()
  const { harness, name } = useAssistantAgent()
  return (
    <AcpRequestCard
      sessionID={assistantSessionID}
      request={request}
      who={name}
      harness={harness}
      canAnswer
      answering={{ busy, error, answer: (optionID, values) => void run(optionID, () => api.assistantAnswer(request.id, optionID, values)) }}
    />
  )
}

export function paramRows(params: unknown): [string, string][] {
  if (params === null || typeof params !== 'object' || Array.isArray(params)) {
    return params === undefined || params === null ? [] : [['Params', JSON.stringify(params)]]
  }
  return Object.entries(params).map(([key, value]) => {
    const words = key.replace(/_id$/, '').replace(/_/g, ' ')
    return [words.charAt(0).toUpperCase() + words.slice(1), typeof value === 'string' ? value : JSON.stringify(value)]
  })
}

export function ConfirmationCard({ confirmation }: { confirmation: AssistantConfirmation }) {
  const { busy, error, run } = useAction()
  const { harness } = useAssistantAgent()
  const decide = (approve: boolean) => void run(approve ? 'approve' : 'decline', () => api.assistantConfirm(confirmation.id, approve))
  const launch = useLaunchRows(confirmation.method, confirmation.params, confirmation.defaults)
  const params = paramRows(confirmation.params)
  const autoApprove = useStore((s) => s.assistantStatus?.auto_approve ?? false)
  const remaining = useSecondsUntil(confirmation.expires_at)
  const total = Date.parse(confirmation.expires_at) - Date.parse(confirmation.created_at)
  return (
    <RequestCard
      aria-label={`Approve this action? ${confirmation.summary}`}
      avatar={<IconTile aria-hidden><AgentGlyph agent={harness} className="size-4" /></IconTile>}
      title={launch ? launchTitle(confirmation.summary) : confirmation.summary}
      source={<Code>{confirmation.method}</Code>}
      meta={<>expires <RelativeTime at={confirmation.expires_at} /></>}
      timeBar={remaining > 0 && total > 0 && <RequestTimeBar left={remaining * 1000} total={total} />}
      footer={(
        <RequestFooter>
          <RequestActions>
            <RequestAction label={busy === 'decline' ? 'Declining…' : 'Decline'} disabled={busy !== null} onClick={() => decide(false)} />
            <RequestAction label={busy === 'approve' ? 'Approving…' : 'Approve'} variant="primary" disabled={busy !== null} onClick={() => decide(true)} />
          </RequestActions>
          {error && <p role="alert" className="text-ui-sm text-state-failed">{error}</p>}
          {confirmation.other_member ? (
            <p className="text-ui-xs text-muted">
              {isLaunch(confirmation.method)
                ? `${confirmation.other_owner}'s account pays for this launch, so this asks even with auto-approval on.`
                : `${confirmation.other_owner} owns this run, so this asks even with auto-approval on.`}
            </p>
          ) : !autoApprove && (
            <p className="text-ui-xs"><AssistantSettingsLink>Skip these cards…</AssistantSettingsLink></p>
          )}
        </RequestFooter>
      )}
    >
      {launch ? (
        <RequestSection label="What starts">
          <dl className="flex min-w-0 flex-col text-ui-sm">
            {launch.map(([key, value]) => (
              <div key={key} className="flex min-w-0 gap-3 py-0.5">
                <dt className="w-24 shrink-0 text-muted">{key}</dt>
                <dd className="max-h-32 min-w-0 flex-1 overflow-y-auto break-words whitespace-pre-wrap">{value}</dd>
              </div>
            ))}
          </dl>
        </RequestSection>
      ) : params.length > 0 && (
        <RequestSection label="Details" count={params.length}>
          <RequestRows
            flush
            rows={params.map(([key, value]) => (
              <div key={key} className="flex min-w-0 gap-3 py-0.5 text-ui-sm">
                <span className="w-20 shrink-0 text-muted">{key}</span>
                <span className="max-h-24 min-w-0 flex-1 overflow-y-auto break-words whitespace-pre-wrap">{value}</span>
              </div>
            ))}
          />
        </RequestSection>
      )}
    </RequestCard>
  )
}
