import { useState } from 'react'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import type { AssistantSettings } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { autoApproveWarning } from '@/routes/settings/assistant'
import { useStore } from '@/store'

function show(s: AssistantSettings) {
  const status = useStore.getState().assistantStatus
  if (status) useStore.getState().setAssistantStatus({ ...status, ...s })
}

/** Asks once, after sign-in, whether the assistant asks before acting; either answer is stored as the member's choice. */
export function ApprovalChoice() {
  const chosen = useStore((s) => s.assistantStatus?.approval_chosen)
  const [sending, setSending] = useState<'ask' | 'act' | null>(null)
  const [error, setError] = useState<string | null>(null)
  if (chosen !== false) return null
  const choose = (choice: 'ask' | 'act') => {
    setSending(choice)
    setError(null)
    api.assistantSettings({ auto_approve: choice === 'act' }).then(show, (err: unknown) => setError(errorSentence(err))).finally(() => setSending(null))
  }
  return (
    <Callout
      tone="neutral"
      role="group"
      aria-label="How should your assistant act?"
      title="How should your assistant act?"
      className="mx-3 mt-3 shrink-0 border border-seam"
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-col items-start gap-1">
          <Button size="sm" variant="primary" disabled={sending !== null} onClick={() => choose('ask')}>
            {sending === 'ask' ? 'Saving…' : 'Ask before acting'}
          </Button>
          <p className="text-ui-sm text-muted">It shows a card before it launches, messages or stops a session, or saves your environment. Recommended.</p>
        </div>
        <div className="flex flex-col items-start gap-1">
          <Button size="sm" variant="secondary" disabled={sending !== null} onClick={() => choose('act')}>
            {sending === 'act' ? 'Turning on…' : 'Act without asking'}
          </Button>
          <p className="text-ui-sm text-muted">{autoApproveWarning} Messaging or stopping another member’s session still asks.</p>
        </div>
        {error && <p role="alert" className="text-ui-sm text-state-failed">{error}</p>}
      </div>
    </Callout>
  )
}

/** Today's confirm for turning auto-approval on, opened by a card's Approve everything from now on after it approved the card. */
export function AutoApproveConfirm() {
  const open = useStore((s) => s.assistantConfirmAutoApprove)
  const close = useStore((s) => s.setAssistantConfirmAutoApprove)
  if (!open) return null
  return (
    <Confirm
      title="Let the assistant act without asking?"
      description={autoApproveWarning}
      action="Turn on"
      pending="Turning on…"
      onConfirm={() => api.assistantSettings({ auto_approve: true }).then(show)}
      onClose={() => close(false)}
    />
  )
}
