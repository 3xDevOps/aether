import { Fragment, useState } from 'react'
import { AssistantSettingsLink } from '@/components/assistant/suggestion'
import { Button } from '@/components/ui/button'
import { Code, CodeBlock } from '@/components/ui/code'
import { RelativeTime } from '@/components/ui/relative-time'
import { RequestCard } from '@/components/ui/request-card'
import { api } from '@/lib/api'
import type { AssistantConfirmation } from '@/lib/assistant-types'
import { message } from '@/lib/format'
import { useKeybindings } from '@/lib/keybindings'
import type { SessionOption, SessionRequest } from '@/lib/session-types'
import { FormFields, missingRequired } from '@/routes/run/session-form'
import { optionVariant, orderedOptions, requestTitle, useToolCall } from '@/routes/run/session-requests'
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

export function AssistantRequestCard({ request }: { request: SessionRequest }) {
  const { busy, error, run } = useAction()
  const call = useToolCall(assistantSessionID, request.tool_call_id)
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [focused, setFocused] = useState(false)
  const options = orderedOptions(request.options ?? [])
  const incomplete = (option: SessionOption) => request.kind === 'question' && option.id === 'accept' && missingRequired(request.schema, values)
  const pick = (index: number) => () => {
    const option = options[index]
    if (!option || incomplete(option)) return
    void run(option.id, () => api.assistantAnswer(request.id, option.id, option.id === 'accept' && request.kind === 'question' ? values : undefined))
  }
  const keyed = (index: number) => options[index]?.kind !== 'allow_always' || index > 0
  useKeybindings('request', focused ? {
    'request-option-1': keyed(0) ? pick(0) : () => {},
    'request-option-2': pick(1),
    'request-option-3': pick(2),
    'request-option-4': pick(3),
  } : {})
  const command = call?.tool_kind === 'execute' ? call.title : undefined
  const title = requestTitle(request, call)

  return (
    <RequestCard
      title={title}
      role="group"
      aria-label={`${title} ${request.title}`}
      onFocus={() => setFocused(true)}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setFocused(false)
      }}
      actions={
        <div className="flex w-full flex-col gap-2">
          {request.kind === 'question' && <FormFields schema={request.schema} values={values} onChange={setValues} />}
          {request.kind === 'link' && request.url && (
            <a href={request.url} target="_blank" rel="noreferrer" className="min-w-0 truncate font-code text-ui-sm text-accent hover:underline">
              {request.url}
            </a>
          )}
          <div className="flex flex-wrap items-center gap-2">
            {options.map((option, index) => (
              <Button
                key={option.id}
                size="sm"
                variant={optionVariant(option, options)}
                disabled={busy !== null || incomplete(option)}
                hint={keyed(index) ? `Press ${index + 1}` : undefined}
                onClick={pick(index)}
              >
                {busy === option.id ? 'Sending…' : option.name}
              </Button>
            ))}
          </div>
          {error && <p role="alert" className="text-ui-sm text-state-failed">{error}</p>}
        </div>
      }
    >
      {request.kind === 'permission' && command ? <CodeBlock>{command}</CodeBlock> : request.title}
    </RequestCard>
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
  const decide = (approve: boolean) => void run(approve ? 'approve' : 'decline', () => api.assistantConfirm(confirmation.id, approve))
  const params = paramRows(confirmation.params)
  const autoApprove = useStore((s) => s.assistantStatus?.auto_approve ?? false)
  return (
    <RequestCard
      title="Approve this action?"
      role="group"
      aria-label={`Approve this action? ${confirmation.summary}`}
      meta={<>expires <RelativeTime at={confirmation.expires_at} /></>}
      actions={
        <div className="flex w-full flex-col gap-2">
          <p className="text-ui-sm text-muted">
            <Code>{confirmation.method}</Code>
          </p>
          {params.length > 0 && (
            <dl className="grid max-h-48 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 overflow-y-auto text-ui-sm [&_dt]:text-muted">
              {params.map(([key, value]) => (
                <Fragment key={key}>
                  <dt>{key}</dt>
                  <dd className="min-w-0 break-words whitespace-pre-wrap">{value}</dd>
                </Fragment>
              ))}
            </dl>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" disabled={busy !== null} onClick={() => decide(true)}>
              {busy === 'approve' ? 'Approving…' : 'Approve'}
            </Button>
            <Button size="sm" variant="secondary" disabled={busy !== null} onClick={() => decide(false)}>
              {busy === 'decline' ? 'Declining…' : 'Decline'}
            </Button>
          </div>
          {error && <p role="alert" className="text-ui-sm text-state-failed">{error}</p>}
          {confirmation.other_member ? (
            <p className="text-ui-sm text-muted">{confirmation.other_owner} owns this run, so this asks even with auto-approval on.</p>
          ) : !autoApprove && (
            <p className="text-ui-sm"><AssistantSettingsLink>Skip these cards…</AssistantSettingsLink></p>
          )}
        </div>
      }
    >
      {confirmation.summary}
    </RequestCard>
  )
}
