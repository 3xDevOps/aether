import { useEffect, useState } from 'react'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import type { AssistantSettings, AssistantSettingsParams, AssistantStatus } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { useStore } from '@/store'

export function AssistantSettingsLink({ children }: { children: React.ReactNode }) {
  const navigate = useStore((s) => s.navigate)
  return (
    <Button variant="link" size="sm" className="h-auto p-0" onClick={() => navigate('settings', { section: 'assistant' })}>
      {children}
    </Button>
  )
}

/** Whether the panel may suggest auto-approval: the server says so and the assistant is idle with nothing waiting on the member. */
export function suggestionShown(status: AssistantStatus | null, busy: boolean, hasRows: boolean): boolean {
  return status?.suggest_auto_approve === true && status.state === 'idle' && !busy && hasRows
}

type Answer = 'on' | 'later' | 'never'

const answers: Record<Answer, AssistantSettingsParams> = {
  on: { auto_approve: true },
  later: { suggestion: 'later' },
  never: { suggestion: 'never' },
}

function Answered({ answer }: { answer: Answer }) {
  const settings = <AssistantSettingsLink>Settings → Assistant</AssistantSettingsLink>
  return (
    <p role="status" className="mx-3 mb-2 shrink-0 text-ui-sm text-muted">
      {answer === 'on' && <>Auto-approval is on. Turn it off any time in {settings}.</>}
      {answer === 'later' && <>You can turn this on later in {settings}.</>}
      {answer === 'never' && <>We won't suggest this again. Auto-approval stays available in {settings}.</>}
    </p>
  )
}

export function AutoApproveSuggestion({ busy, hasRows }: { busy: boolean; hasRows: boolean }) {
  const status = useStore((s) => s.assistantStatus)
  const [answered, setAnswered] = useState<Answer | null>(null)
  const [sending, setSending] = useState<Answer | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (busy) setAnswered(null)
  }, [busy])

  if (answered) return <Answered answer={answered} />
  if (!suggestionShown(status, busy, hasRows)) return null

  const answer = (choice: Answer) => {
    setSending(choice)
    setError(null)
    api.assistantSettings(answers[choice]).then((s: AssistantSettings) => {
      const current = useStore.getState().assistantStatus
      if (current) useStore.getState().setAssistantStatus({ ...current, ...s })
      setAnswered(choice)
    }, (err: unknown) => setError(errorSentence(err))).finally(() => setSending(null))
  }
  const button = (choice: Answer, label: string) => (
    <Button size="sm" variant="secondary" disabled={sending !== null} onClick={() => answer(choice)}>{label}</Button>
  )

  return (
    <Callout
      tone="neutral"
      role="group"
      aria-label="Skip approval cards for the assistant?"
      title="Skip approval cards for the assistant?"
      className="mx-3 mb-2 shrink-0 border border-seam"
      actions={(
        <>
          {button('on', sending === 'on' ? 'Turning on…' : 'Turn on auto-approval')}
          {button('later', 'Not now')}
          {button('never', "Don't suggest again")}
        </>
      )}
    >
      <p>
        You've approved the last few actions yourself. With auto-approval on, the assistant launches, messages and stops your runs and saves your environment without a card. Each action is still noted here before it runs. Text the assistant reads from runs, files or web pages could steer it, so turn this on only if you trust what it reads.
      </p>
      <p className="mt-1 text-muted">Still asks: anything that touches another member's run.</p>
      {error && <p role="alert" className="mt-1 text-state-failed">{error}</p>}
    </Callout>
  )
}
