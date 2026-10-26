import { useRef, useState } from 'react'
import type * as React from 'react'
import { Wrench } from '@/components/icons'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Code } from '@/components/ui/code'
import { IconTile } from '@/components/ui/icon-tile'
import { RequestCard } from '@/components/ui/request-card'
import { RequestAction, RequestActions, RequestFooter, RequestPills } from '@/components/ui/request-choice'
import { RequestSection } from '@/components/ui/request-section'
import { toolIcon } from '@/components/ui/timeline'
import { api } from '@/lib/api'
import { useHeightGlide } from '@/lib/flip'
import { message } from '@/lib/format'
import { useKeybindings } from '@/lib/keybindings'
import type { Motion } from '@/lib/motion'
import type { SessionItem, SessionRequest, SessionToolCall } from '@/lib/session-types'
import { cn } from '@/lib/utils'
import { useRunAgent } from '@/routes/run/agent-context'
import { useImplicitControl, type AgentTerminal } from '@/routes/run/agent-terminal'
import { FormFields, missingRequired } from '@/routes/run/session-form'
import { answerWord, type Choice, choicePlan, type Headline, plainHeadline, requestHeadline } from '@/routes/run/request-options'
import { LinkSection, PermissionSections, requestDescription, useWholeItem } from '@/routes/run/request-preview'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'
import type { AcpSession } from '@/store/sessions'
import { sessionLease } from '@/store/session-stream'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'
import { agentLabel } from '@/store/subagents'

export function AnsweredText({ request, command }: { request: SessionRequest; command?: string }) {
  return (
    <>
      <span className="font-medium">{answerWord(request)}</span>
      {' · '}
      {command ? <>run <Code>{command}</Code></> : request.title}
    </>
  )
}

/**
 * The tool call a request asks about. A subagent's calls live in the agents
 * index rather than the main turns, so `owner` names the subagent then.
 */
export function findToolItem(session: AcpSession | undefined, id: string | undefined): { item: SessionItem; owner?: string } | undefined {
  if (!session || !id) return undefined
  for (let t = session.turns.length - 1; t >= 0; t--) {
    const items = session.turns[t]!.items
    for (let i = items.length - 1; i >= 0; i--) {
      if (items[i]!.tool_call?.id === id) return { item: items[i]! }
    }
  }
  for (const [owner, agent] of Object.entries(session.agents.agents)) {
    for (let i = agent.items.length - 1; i >= 0; i--) {
      if (agent.items[i]!.tool_call?.id === id) return { item: agent.items[i]!, owner }
    }
  }
  return undefined
}

export function useToolItem(sessionID: string, id: string | undefined): { item?: SessionItem; subagent?: string } {
  const item = useStore((s) => findToolItem(s.acpSessions[sessionID], id)?.item)
  const subagent = useStore((s) => {
    const session = s.acpSessions[sessionID]
    const owner = findToolItem(session, id)?.owner
    return owner && session ? agentLabel(session.agents.agents[owner]!) : undefined
  })
  return { item, subagent }
}

export function requestAnnouncement(session: AcpSession | undefined, request: SessionRequest, who: string): string {
  const found = findToolItem(session, request.tool_call_id)
  const owner = found?.owner && session?.agents.agents[found.owner]
  return `${plainHeadline(requestHeadline(request, found?.item.tool_call, owner ? agentLabel(owner) : who))} ${request.title}`
}

export function HeadlineText({ headline }: { headline: Headline }) {
  return <>{headline.who} <span className="font-normal text-muted">{headline.verb}</span> {headline.rest}</>
}

function Source({ call }: { call: SessionToolCall }) {
  const Icon = toolIcon[call.tool_kind ?? ''] ?? Wrench
  return <><Icon aria-hidden /><span className="min-w-0 truncate">{call.tool_name}</span></>
}

export interface Answering {
  busy: string | null
  error?: string
  answer: (optionID: string, values?: Record<string, unknown>) => void
}

function Choices({ request, call, answering, canAnswer, incomplete, values, focused, aside }: {
  request: SessionRequest
  call: SessionToolCall | undefined
  answering: Answering
  canAnswer: boolean
  incomplete: boolean
  values: Record<string, unknown>
  focused: boolean
  aside?: React.ReactNode
}) {
  const [lasting, setLasting] = useState<'once' | 'always'>('once')
  const late = usePastDeadline(answering.busy !== null)
  const plan = choicePlan(request, call)
  const choices = plan.kind === 'pills' ? [plan.once, plan.always, plan.deny, ...plan.extras].filter((c) => c !== undefined) : plan.choices
  const blocked = (choice: Choice) => !canAnswer || answering.busy !== null || (incomplete && choice.option.id === 'accept')
  const pick = (choice: Choice | undefined) => {
    if (!choice || blocked(choice)) return
    answering.answer(choice.option.id, choice.option.id === 'accept' && request.kind === 'question' ? values : undefined)
  }
  const byDigit = (digit: number) => () => pick(choices.find((c) => c.digit === digit))
  useKeybindings('request', focused ? {
    'request-option-1': byDigit(1),
    'request-option-2': byDigit(2),
    'request-option-3': byDigit(3),
    'request-option-4': byDigit(4),
  } : {})
  const label = (choice: Choice) => (answering.busy === choice.option.id ? (late ? stillWaiting : 'Sending…') : choice.option.name)
  const action = (choice: Choice, variant: 'primary' | 'secondary' | 'danger' | 'quiet', key = choice.option.id) => (
    <RequestAction key={key} label={label(choice)} digit={choice.digit} variant={variant} disabled={blocked(choice)} onClick={() => pick(choice)} />
  )
  const allow = plan.kind === 'pills' ? (lasting === 'once' ? plan.once : plan.always) : undefined
  return (
    <RequestFooter>
      {plan.kind === 'pills' && (
        <>
          <RequestPills
            value={lasting}
            onValueChange={(value) => setLasting(value as 'once' | 'always')}
            disabled={!canAnswer || answering.busy !== null}
            items={[{ value: 'once', label: 'Just once' }, { value: 'always', label: 'Always' }]}
          />
          {lasting === 'always' && <p className="text-ui-xs text-muted">The agent remembers this and stops asking.</p>}
        </>
      )}
      <RequestActions
        extras={(aside || (plan.kind === 'pills' && plan.extras.length > 0)) && (
          <>
            {plan.kind === 'pills' && plan.extras.map((choice) => action(choice, 'quiet'))}
            {aside}
          </>
        )}
        className={cn(plan.kind === 'buttons' && plan.choices.length > 2 && 'max-md:coarse:flex-col max-md:coarse:items-stretch')}
      >
        {plan.kind === 'pills' ? (
          <>
            {plan.deny && action(plan.deny, 'secondary')}
            {allow && action(allow, call?.tool_kind === 'delete' ? 'danger' : 'primary', 'allow')}
          </>
        ) : plan.choices.map((choice) => action(choice, choice.primary ? (call?.tool_kind === 'delete' ? 'danger' : 'primary') : 'secondary'))}
      </RequestActions>
      {answering.error && <p role="alert" className="text-ui-sm text-state-failed">{answering.error}</p>}
    </RequestFooter>
  )
}

const previewGlide: Motion = { duration: '--duration-glide', easing: '--ease-glide' }

export function AcpRequestCard({ sessionID, request, who, harness, answering, canAnswer, aside, meta, id, className }: {
  sessionID: string
  request: SessionRequest
  who: string
  harness: string
  answering: Answering
  canAnswer: boolean
  aside?: React.ReactNode
  meta?: React.ReactNode
  id?: string
  className?: string
}) {
  const { item, subagent } = useToolItem(sessionID, request.tool_call_id)
  const call = item?.tool_call
  const whole = useWholeItem(sessionID, request.kind === 'permission' ? item : undefined)
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [focused, setFocused] = useState(false)
  const card = useRef<HTMLDivElement>(null)
  useHeightGlide(card, `${call ? 'call' : 'none'}:${whole.loading ? 'loading' : 'ready'}`, previewGlide)
  const headline = requestHeadline(request, call, subagent ?? who)
  return (
    <RequestCard
      ref={card}
      id={id}
      className={className}
      onFocus={() => setFocused(true)}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setFocused(false)
      }}
      aria-label={`${plainHeadline(headline)} ${request.title}`}
      avatar={<IconTile aria-hidden><AgentGlyph agent={harness} className="size-4" /></IconTile>}
      title={<HeadlineText headline={headline} />}
      source={call?.tool_name ? <Source call={call} /> : undefined}
      meta={meta}
      description={requestDescription(request, call)}
      footer={(
        <Choices
          request={request}
          call={call}
          answering={answering}
          canAnswer={canAnswer}
          incomplete={request.kind === 'question' && missingRequired(request.schema, values)}
          values={values}
          focused={focused}
          aside={aside}
        />
      )}
    >
      {request.kind === 'permission' && <PermissionSections call={call} whole={whole} />}
      {request.kind === 'question' && <RequestSection><FormFields schema={request.schema} values={values} onChange={setValues} /></RequestSection>}
      {request.kind === 'link' && request.url && <LinkSection url={request.url} />}
    </RequestCard>
  )
}

function useRunAnswer(runID: string, request: SessionRequest): Answering {
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
  return { busy, error, answer: (optionID, values) => void answer(optionID, values) }
}

export function SessionRequestCard({ run, request, agent, meta, id, className }: {
  run: RunRecord
  request: SessionRequest
  agent: AgentTerminal
  meta?: React.ReactNode
  id?: string
  className?: string
}) {
  const answering = useRunAnswer(run.id, request)
  const control = useImplicitControl(run, agent)
  const who = useRunAgent()?.agentName ?? 'The agent'
  return (
    <AcpRequestCard
      id={id}
      className={className}
      sessionID={run.id}
      request={request}
      who={who}
      harness={run.harness}
      canAnswer={control.canAct}
      answering={{ ...answering, answer: (optionID, values) => control.withControl(() => answering.answer(optionID, values)) }}
      meta={meta}
      aside={!control.canAct && agent.steerable && !agent.controlUnavailable && (
        <Button size="sm" variant="link" onClick={agent.session.takeControl}>Take control to answer</Button>
      )}
    />
  )
}
