import { createContext, useContext, useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { api } from '@/lib/api'
import type { AssistantSettings, AssistantSettingsParams } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { useStore } from '@/store'

/** True while the assistant is a modal sheet, which would cover Settings. */
export const AssistantSheet = createContext(false)

export function AssistantSettingsLink({ children }: { children: React.ReactNode }) {
  const navigate = useStore((s) => s.navigate)
  const sheet = useContext(AssistantSheet)
  const open = () => {
    navigate('settings', { section: 'assistant' })
    if (sheet) useStore.getState().setAssistantOpen(false)
  }
  return (
    <Button variant="link" size="sm" className="h-auto p-0" onClick={open}>
      {children}
    </Button>
  )
}

type Answer = 'on' | 'later' | 'never'

const answers: Record<Answer, AssistantSettingsParams> = {
  on: { auto_approve: true },
  later: { suggestion: 'later' },
  never: { suggestion: 'never' },
}

function Answered({ answer }: { answer: Answer }) {
  const line = useRef<HTMLParagraphElement>(null)
  useEffect(() => line.current?.focus(), [])
  const settings = <AssistantSettingsLink>Settings → Assistant</AssistantSettingsLink>
  return (
    <p ref={line} role="status" tabIndex={-1} className="mx-3 mb-2 shrink-0 text-ui-sm text-muted outline-none">
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
  const alert = useRef<HTMLParagraphElement>(null)
  const turn = useRef(0)

  useEffect(() => {
    if (!busy && hasRows) return
    turn.current++
    setAnswered(null)
  }, [busy, hasRows])
  useEffect(() => {
    if (error) alert.current?.focus()
  }, [error])

  if (answered) return hasRows && !busy ? <Answered answer={answered} /> : null
  if (status?.suggest_auto_approve !== true || status.state !== 'idle' || busy || !hasRows) return null

  const answer = (choice: Answer) => {
    const identity = useStore.getState().identityKey
    const current = () => useStore.getState().identityKey === identity
    const asked = turn.current
    setSending(choice)
    setError(null)
    api.assistantSettings(answers[choice]).then((s: AssistantSettings) => {
      if (!current()) return
      const latest = useStore.getState().assistantStatus
      if (latest) useStore.getState().setAssistantStatus({ ...latest, ...s })
      if (turn.current === asked) setAnswered(choice === 'later' && s.suggestion_off ? 'never' : choice)
    }, (err: unknown) => {
      if (current()) setError(errorSentence(err))
    }).finally(() => setSending(null))
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
      className="mx-3 mb-2 max-h-[40dvh] shrink-0 overflow-y-auto border border-seam"
      actions={(
        <>
          {button('on', sending === 'on' ? 'Turning on…' : 'Turn on auto-approval')}
          {button('later', 'Not now')}
          {button('never', "Don't suggest again")}
        </>
      )}
    >
      <p>
        You've approved the last few actions yourself. With auto-approval on, the assistant launches, messages and stops your sessions and saves your environment without a card. Each action is still noted here before it runs. Text the assistant reads from sessions, files or web pages could steer it, so turn this on only if you trust what it reads.
      </p>
      <p className="mt-1 text-muted">Still asks: messaging or stopping another member's session.</p>
      {error && <p ref={alert} role="alert" tabIndex={-1} className="mt-1 text-state-failed outline-none">{error}</p>}
    </Callout>
  )
}
