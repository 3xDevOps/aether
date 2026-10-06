import { useState } from 'react'
import type * as React from 'react'
import { ChevronLeft, ChevronRight, Copy, ExternalLink } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Code, CodeBlock } from '@/components/ui/code'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { RequestCard } from '@/components/ui/request-card'
import { api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { useKeybindings } from '@/lib/keybindings'
import type { SessionOption, SessionRequest, SessionToolCall } from '@/lib/session-types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useStore } from '@/store'
import { sessionLease } from '@/store/session-stream'
import { cn } from '@/lib/utils'

export function requestTitle(request: SessionRequest, call?: SessionToolCall): string {
  if (request.kind === 'question') return 'The agent asks a question'
  if (request.kind === 'link') return 'The agent asks you to open a link'
  switch (call?.tool_kind) {
    case 'execute':
      return 'Allow this command?'
    case 'edit':
    case 'delete':
    case 'move':
      return 'Allow this file change?'
    case 'fetch':
      return 'Allow this web request?'
    case 'switch_mode':
      return 'Change how the agent works?'
    default:
      return 'Allow this action?'
  }
}

function answerWord(request: SessionRequest): string {
  if (request.status === 'cancelled') return 'Cancelled'
  if (request.kind !== 'permission') return request.answer === 'accept' ? 'Answered' : 'Declined'
  const option = request.options?.find((o) => o.id === request.answer)
  if (option?.kind === 'allow_always') return 'Always allowed'
  if (option?.kind?.startsWith('allow')) return 'Approved'
  if (option?.kind?.startsWith('reject')) return 'Denied'
  return option ? `Answered “${option.name}”` : 'Answered'
}

export function AnsweredText({ request, command }: { request: SessionRequest; command?: string }) {
  return (
    <>
      {answerWord(request)}: {command ? <>run <Code>{command}</Code></> : request.title}
    </>
  )
}

interface FieldSchema {
  type?: string
  title?: string
  description?: string
  enum?: string[]
}

function fieldsOf(schema: unknown): [string, FieldSchema][] {
  const properties = (schema as { properties?: Record<string, FieldSchema> } | undefined)?.properties
  return properties ? Object.entries(properties) : []
}

function FormFields({ schema, values, onChange }: {
  schema: unknown
  values: Record<string, unknown>
  onChange: (values: Record<string, unknown>) => void
}) {
  return (
    <div className="flex flex-col gap-2">
      {fieldsOf(schema).map(([name, field]) => {
        const label = field.title ?? name
        if (field.type === 'boolean') {
          return (
            <label key={name} className="flex items-center gap-2 text-ui">
              <Checkbox checked={values[name] === true} onCheckedChange={(checked) => onChange({ ...values, [name]: checked === true })} />
              {label}
            </label>
          )
        }
        const numeric = field.type === 'number' || field.type === 'integer'
        return (
          <FormField key={name} label={label} help={field.enum ? `One of: ${field.enum.join(', ')}` : field.description}>
            <Input
              type={numeric ? 'number' : 'text'}
              value={String(values[name] ?? '')}
              onChange={(event) => onChange({ ...values, [name]: numeric ? Number(event.target.value) : event.target.value })}
            />
          </FormField>
        )
      })}
    </div>
  )
}

function optionVariant(option: SessionOption, options: SessionOption[]): 'primary' | 'secondary' {
  const primary = options.find((o) => o.kind === 'allow_once') ?? options.find((o) => o.id === 'accept') ?? options[0]
  return option === primary ? 'primary' : 'secondary'
}

function useAnswer(runID: string, request: SessionRequest) {
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string>()
  const answer = async (optionID: string, values?: Record<string, unknown>) => {
    const lease = sessionLease(useStore, runID)
    if (!lease || busy) return
    setBusy(optionID)
    setError(undefined)
    try {
      await api.runInputAnswer(runID, request.id, optionID, lease, values)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(null)
    }
  }
  return { busy, error, answer }
}

function useToolCall(runID: string, id: string | undefined): SessionToolCall | undefined {
  return useStore((s) => {
    const turns = s.acpSessions[runID]?.turns ?? []
    for (let t = turns.length - 1; id && t >= 0; t--) {
      const items = turns[t]!.items
      for (let i = items.length - 1; i >= 0; i--) {
        if (items[i]!.tool_call?.id === id) return items[i]!.tool_call
      }
    }
    return undefined
  })
}

export function SessionRequestCard({ runID, request, agent, position, id, className }: {
  runID: string
  request: SessionRequest
  agent: AgentTerminal
  position?: { index: number; total: number; step: (delta: number) => void }
  id?: string
  className?: string
}) {
  const { busy, error, answer } = useAnswer(runID, request)
  const call = useToolCall(runID, request.tool_call_id)
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [focused, setFocused] = useState(false)
  const options = request.options ?? []
  const canAnswer = agent.localControl
  const pick = (index: number) => () => {
    const option = options[index]
    if (option && canAnswer) void answer(option.id, option.id === 'accept' && request.kind === 'question' ? values : undefined)
  }
  useKeybindings('request', focused ? {
    'request-option-1': pick(0),
    'request-option-2': pick(1),
    'request-option-3': pick(2),
    'request-option-4': pick(3),
  } : {})

  const command = call?.tool_kind === 'execute' ? call.title : undefined
  const body: React.ReactNode = request.kind === 'permission'
    ? command ? <CodeBlock>{command}</CodeBlock> : request.title
    : request.title

  return (
    <RequestCard
      id={id}
      title={requestTitle(request, call)}
      aria-label={`${requestTitle(request, call)} ${request.title}`}
      className={className}
      onFocus={() => setFocused(true)}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setFocused(false)
      }}
      meta={position && position.total > 1 && (
        <span className="flex items-center gap-0.5">
          <Button variant="ghost" size="icon-sm" label="Previous request" onClick={() => position.step(-1)}><ChevronLeft /></Button>
          {position.index + 1}/{position.total}
          <Button variant="ghost" size="icon-sm" label="Next request" onClick={() => position.step(1)}><ChevronRight /></Button>
        </span>
      )}
      actions={
        <div className="flex w-full flex-col gap-2">
          {request.kind === 'question' && <FormFields schema={request.schema} values={values} onChange={setValues} />}
          {request.kind === 'link' && request.url && (
            <div className="flex min-w-0 items-center gap-2">
              <a href={request.url} target="_blank" rel="noreferrer" className="min-w-0 flex-1 truncate font-code text-ui-sm text-accent hover:underline">
                {request.url}
              </a>
              <Button variant="ghost" size="icon-sm" label="Copy link" onClick={(event) => void copyText(request.url!, event.currentTarget)}><Copy /></Button>
              <Button variant="secondary" size="sm" onClick={() => window.open(request.url, '_blank', 'noreferrer')}><ExternalLink />Open</Button>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-2 max-md:coarse:flex-col max-md:coarse:items-stretch">
            {options.map((option, index) => (
              <Button
                key={option.id}
                size="sm"
                variant={optionVariant(option, options)}
                disabled={!canAnswer || busy !== null}
                hint={focused ? `Press ${index + 1}` : undefined}
                onClick={pick(index)}
              >
                {busy === option.id ? 'Sending…' : option.name}
              </Button>
            ))}
            {!canAnswer && agent.steerable && !agent.controlUnavailable && (
              <Button size="sm" variant="link" onClick={agent.session.takeControl}>Take control to answer</Button>
            )}
          </div>
          {error && <p role="alert" className="text-ui-sm text-state-failed">{error}</p>}
        </div>
      }
    >
      {request.kind !== 'link' || !request.url ? body : request.title}
    </RequestCard>
  )
}

export const dockedRequestID = 'session-request-docked'

export function RequestDock({ runID, requests, agent, className }: {
  runID: string
  requests: SessionRequest[]
  agent: AgentTerminal
  className?: string
}) {
  const [index, setIndex] = useState(0)
  if (requests.length === 0) return null
  const current = Math.min(index, requests.length - 1)
  const request = requests[current]!
  return (
    <div className={cn('overflow-y-auto', className)}>
      <SessionRequestCard
        key={request.id}
        id={dockedRequestID}
        runID={runID}
        request={request}
        agent={agent}
        position={{ index: current, total: requests.length, step: (delta) => setIndex((current + delta + requests.length) % requests.length) }}
      />
    </div>
  )
}
