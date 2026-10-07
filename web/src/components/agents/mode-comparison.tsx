import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import { type ReactNode, useId } from 'react'
import { enhancedUnavailable, label, modeLines } from '@/components/agents/agent-copy'
import { mockMoment } from '@/components/agents/mock-moment'
import { SessionMock } from '@/components/agents/session-mock'
import { TerminalMock } from '@/components/agents/terminal-mock'
import type { AgentInfo } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'

export type SetupMode = 'tui' | 'acp'

const copy = {
  tui: {
    title: 'Standard',
    description: "Your agent's own terminal, exactly as on your machine.",
    tradeOff: 'No structured view; the agent acts without asking, and anything it asks is answered in the terminal.',
  },
  acp: {
    title: 'Enhanced',
    description: 'Aether reads what your agent is doing instead of only showing its terminal: messages, tool activity, file changes, approvals and progress appear as native controls.',
    tradeOff: "Runs through an adapter, not the agent's own screen; some agent-specific commands and screens are missing; starts a few seconds slower.",
  },
} satisfies Record<SetupMode, { title: string; description: string; tradeOff: string }>

const mocks: Record<SetupMode, ReactNode> = {
  tui: <TerminalMock items={mockMoment} />,
  acp: <SessionMock items={mockMoment} />,
}

function CardBody({ id, mode, note, disabled, marker }: { id: string; mode: SetupMode; note: string; disabled?: boolean; marker?: ReactNode }) {
  const { title, description, tradeOff } = copy[mode]
  return (
    <>
      <span className="flex items-center gap-2">
        {marker}
        <span id={`${id}-title`} className={cn('text-title', disabled ? 'text-muted' : 'text-text')}>
          {title}
        </span>
      </span>
      {mocks[mode]}
      <span id={`${id}-description`} className={cn('text-ui', disabled ? 'text-muted' : 'text-text')}>
        {description}
      </span>
      <span id={`${id}-trade-off`} className="text-ui-sm text-muted">
        <span className="font-medium text-text">Trade-off: </span>
        {tradeOff}
      </span>
      <span id={`${id}-note`} className={cn('text-ui-sm', disabled ? 'text-text' : 'text-muted')}>
        {note}
      </span>
    </>
  )
}

const card = 'flex min-w-0 flex-col gap-2 rounded-panel border border-seam p-3 text-left'

function ModeCard({ value, note, disabled }: { value: SetupMode; note: string; disabled?: boolean }) {
  const id = useId()
  return (
    <RadioGroupPrimitive.Item
      value={value}
      disabled={disabled}
      aria-labelledby={`${id}-title`}
      aria-describedby={`${id}-description ${id}-trade-off ${id}-note`}
      className={cn(
        focusRing,
        card,
        'group cursor-pointer transition-colors duration-100 motion-reduce:transition-none',
        'hover:enabled:bg-hover data-[state=checked]:border-accent data-[state=checked]:bg-hover disabled:cursor-not-allowed',
      )}
    >
      <CardBody
        id={id}
        mode={value}
        note={note}
        disabled={disabled}
        marker={
          <span className="grid size-4 shrink-0 place-items-center rounded-full border border-control bg-canvas group-data-[state=checked]:border-accent group-disabled:border-seam">
            <span className="size-2 rounded-full bg-accent opacity-0 group-data-[state=checked]:opacity-100" />
          </span>
        }
      />
    </RadioGroupPrimitive.Item>
  )
}

function OverviewCard({ mode, note }: { mode: SetupMode; note: string }) {
  const id = useId()
  return (
    <li className={card}>
      <CardBody id={id} mode={mode} note={note} />
    </li>
  )
}

export function ModeOverview() {
  return (
    <div className="@container min-w-0">
      <ul aria-label="Standard and Enhanced" className="grid min-w-0 gap-3 @min-[640px]:grid-cols-2">
        <OverviewCard mode="tui" note="Works with every agent." />
        <OverviewCard mode="acp" note="Works with agents that speak the Agent Client Protocol, natively or through an adapter. The agent list says which do." />
      </ul>
    </div>
  )
}

export function ModeComparison({
  agent,
  value,
  onChange,
}: {
  agent: AgentInfo
  value: SetupMode
  onChange: (mode: SetupMode) => void
}) {
  const unavailable = enhancedUnavailable(agent)
  const lines = modeLines(agent)
  return (
    <div className="@container flex min-w-0 flex-col gap-3">
      <RadioGroupPrimitive.Root
        aria-label={`How runs show ${label(agent)}`}
        value={value}
        onValueChange={(next) => onChange(next as SetupMode)}
        className="grid gap-3 @min-[640px]:grid-cols-2"
      >
        <ModeCard value="tui" note="Works with every agent." />
        <ModeCard
          value="acp"
          note={unavailable ?? 'Runs with full permissions by default, like Standard; switch its mode in the run to be asked.'}
          disabled={unavailable !== null}
        />
      </RadioGroupPrimitive.Root>
      {lines.length > 0 && (
        <dl aria-label={`Enhanced for ${label(agent)}`} className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm">
          {lines.map((line) => (
            <div key={line.term} className="contents">
              <dt className="text-muted">{line.term}</dt>
              <dd className="text-text">{line.text}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  )
}
