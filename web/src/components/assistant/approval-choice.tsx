import { useState } from 'react'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import type { AssistantSettings } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { autoApproveWarning } from '@/routes/settings/assistant'
import { useStore } from '@/store'

/** Merges settings read for the member signed in as `identity`, unless another member signed in since. */
export function showSettings(identity: string | null, s: AssistantSettings) {
  const store = useStore.getState()
  if (store.identityKey === identity && store.assistantStatus) store.setAssistantStatus({ ...store.assistantStatus, ...s })
}

/** Asks once, after sign-in, whether the assistant asks before acting; either answer is stored as the member's choice. */
export function ApprovalChoice() {
  // A failed status carries zero settings, not the stored choice.
  const unasked = useStore((s) => s.assistantStatus?.approval_chosen === false && s.assistantStatus.state !== 'failed')
  const [sending, setSending] = useState<'ask' | 'act' | null>(null)
  const [error, setError] = useState<string | null>(null)
  if (!unasked) return null
  const choose = (choice: 'ask' | 'act') => {
    const identity = useStore.getState().identityKey
    setSending(choice)
    setError(null)
    api.assistantSettings({ auto_approve: choice === 'act' }).then((s) => showSettings(identity, s), (err: unknown) => {
      if (useStore.getState().identityKey === identity) setError(errorSentence(err))
    }).finally(() => setSending(null))
  }
  return (
    <Callout
      tone="neutral"
      role="group"
      aria-label="How should your assistant act?"
      title="How should your assistant act?"
      className="mx-3 mt-3 max-h-[40dvh] shrink-0 overflow-y-auto border border-seam"
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-col items-start gap-1">
          <Button size="sm" variant="primary" disabled={sending !== null} onClick={() => choose('ask')}>
            {sending === 'ask' ? 'Saving…' : 'Ask before acting'}
          </Button>
          <p className="text-ui-sm text-muted">It shows a card before it launches, messages or stops a session, or saves your environment. Rules you save with Always approve still skip their card. Recommended.</p>
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
      onConfirm={() => {
        const identity = useStore.getState().identityKey
        return api.assistantSettings({ auto_approve: true }).then((s) => showSettings(identity, s))
      }}
      onClose={() => close(false)}
    />
  )
}
