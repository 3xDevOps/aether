import { useState } from 'react'
import { toast } from 'sonner'
import { originLabel } from '@/components/assistant/labels'
import { isLaunch, launchTitle, useLaunchRows } from '@/components/assistant/launch-summary'
import { AssistantSettingsLink } from '@/components/assistant/suggestion'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { RelativeTime } from '@/components/ui/relative-time'
import { IconTile } from '@/components/ui/icon-tile'
import { RequestCard, RequestTimeBar } from '@/components/ui/request-card'
import { RequestAction, RequestActions, RequestFooter } from '@/components/ui/request-choice'
import { RequestRows, RequestSection } from '@/components/ui/request-section'
import { api } from '@/lib/api'
import type { AssistantConfirmation } from '@/lib/assistant-types'
import { message } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useSecondsUntil } from '@/lib/hooks'
import type { SessionRequest } from '@/lib/session-types'
import { AcpRequestCard } from '@/routes/run/session-requests'
import { useStore } from '@/store'
import { assistantSessionID, cardPending } from '@/store/assistant'

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
    const words = key.replace(/_id$/, '').replace(/_/g, ' ').replace(/\bchild runs?\b|\bruns?\b/g, (m) => (m.startsWith('child') ? 'subsession' : 'session') + (m.endsWith('s') ? 's' : ''))
    return [words.charAt(0).toUpperCase() + words.slice(1), typeof value === 'string' ? value : JSON.stringify(value)]
  })
}

/** How many cards the dock holds and what each waits on. */
export function cardsHeading(confirmations: AssistantConfirmation[]): string {
  const waiting = confirmations.filter(cardPending).length
  const running = confirmations.length - waiting
  const parts = [waiting && `${waiting} ${waiting === 1 ? 'waits' : 'wait'} for you`, running && `${running} running`].filter(Boolean)
  return `${confirmations.length} ${confirmations.length === 1 ? 'card' : 'cards'}: ${parts.join(', ')}`
}

export function ConfirmationCard({ confirmation }: { confirmation: AssistantConfirmation }) {
  const { busy, error, run } = useAction()
  const { harness } = useAssistantAgent()
  const decide = (approve: boolean) => void run(approve ? 'approve' : 'decline', () => api.assistantConfirm(confirmation.id, approve))
  const rule = confirmation.rule
  const approveEverything = () => void run('everything', async () => {
    const identity = useStore.getState().identityKey
    await api.assistantConfirm(confirmation.id, true)
    if (useStore.getState().identityKey === identity) useStore.getState().setAssistantConfirmAutoApprove(true)
  })
  const reason: [string, string][] = confirmation.reason ? [['Reason', confirmation.reason]] : []
  const rows = useLaunchRows(confirmation.method, confirmation.params, confirmation.defaults)
  const launch = rows && [...reason, ...rows]
  const params: [string, string][] = rows ? [] : [...reason, ...(confirmation.task ? [['Task', confirmation.task] as [string, string]] : []), ...paramRows(confirmation.params)]
  const autoApprove = useStore((s) => s.assistantStatus?.auto_approve ?? false)
  const remaining = useSecondsUntil(confirmation.expires_at)
  const total = Date.parse(confirmation.expires_at) - Date.parse(confirmation.created_at)
  const pending = cardPending(confirmation)
  const origin = originLabel(confirmation.origin)
  return (
    <RequestCard
      aria-label={pending ? `Approve this action? ${confirmation.summary}` : `Approved, running: ${confirmation.summary}`}
      avatar={<IconTile aria-hidden><AgentGlyph agent={harness} className="size-4" /></IconTile>}
      title={launch || confirmation.task ? launchTitle(confirmation.summary) : confirmation.summary}
      source={<><Code>{confirmation.method}</Code>{origin && <span className="text-ui-xs text-muted">{origin}</span>}</>}
      meta={pending ? <>expires <RelativeTime at={confirmation.expires_at} /></> : undefined}
      timeBar={pending && remaining > 0 && total > 0 && <RequestTimeBar left={remaining * 1000} total={total} />}
      footer={pending && (
        // The dock scrolls the whole card: its actions come after every row they approve.
        <RequestFooter className="static">
          <RequestActions>
            <RequestAction label={busy === 'decline' ? 'Declining…' : 'Decline'} disabled={busy !== null} onClick={() => decide(false)} />
            <RequestAction label={busy === 'approve' ? 'Approving…' : 'Approve'} variant="primary" disabled={busy !== null} onClick={() => decide(true)} />
          </RequestActions>
          {rule && (
            <div className="flex min-w-0 flex-col items-stretch gap-1.5">
              <p className="text-ui-xs text-muted">
                {rule.suggested ? 'You approved this kind three times in a row. Or save a rule:' : 'Or save a rule:'}
              </p>
              <Button
                size="md"
                variant="secondary"
                className={cn('h-auto min-h-8 w-full min-w-0 justify-start py-1.5 text-left whitespace-normal coarse:h-auto coarse:min-h-11',
                  rule.suggested && 'border-l-2 border-l-accent bg-accent-soft')}
                disabled={busy !== null}
                onClick={() => void run('always', () => api.assistantApproveAlways(confirmation.id).then(() => useStore.getState().noteRuleSaved(), (err: unknown) => {
                  // The server may have approved the card before saving the rule failed, and the card is gone by then.
                  toast.error(`Always approve: ${message(err)}`)
                  throw err
                }))}
              >
                {busy === 'always' ? 'Saving the rule…' : rule.wording}
              </Button>
              {!autoApprove && (
                <Button size="md" variant="quiet" className="justify-start" disabled={busy !== null} onClick={approveEverything}>
                  {busy === 'everything' ? 'Approving…' : 'Approve everything from now on'}
                </Button>
              )}
            </div>
          )}
          {error && <p role="alert" className="text-ui-sm text-state-failed">{error}</p>}
          {confirmation.other_member ? (
            <p className="text-ui-xs text-muted">
              {isLaunch(confirmation.method)
                ? `${confirmation.other_owner}'s account pays for this launch, so this asks even with auto-approval on.`
                : `${confirmation.other_owner} owns this session, so this asks even with auto-approval on.`}
            </p>
          ) : !autoApprove && !rule && (
            <p className="text-ui-xs"><AssistantSettingsLink>Skip these cards…</AssistantSettingsLink></p>
          )}
        </RequestFooter>
      ) || (
        // In the footer, the status leaves the title its full width.
        <RequestFooter className="static">
          <p className="text-ui-sm text-muted">Approved{confirmation.decided_at && <> <RelativeTime at={confirmation.decided_at} /></>}, running…</p>
        </RequestFooter>
      )}
    >
      {launch ? (
        <RequestSection label="What starts">
          <dl className="flex min-w-0 flex-col text-ui-sm">
            {launch.map(([key, value]) => (
              <div key={key} className="flex min-w-0 gap-3 py-0.5">
                <dt className="w-24 shrink-0 text-muted">{key}</dt>
                <dd className="min-w-0 flex-1 break-words whitespace-pre-wrap">{value}</dd>
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
                <span className="min-w-0 flex-1 break-words whitespace-pre-wrap">{value}</span>
              </div>
            ))}
          />
        </RequestSection>
      )}
    </RequestCard>
  )
}
