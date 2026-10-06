import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import { type ReactNode, useId } from 'react'
import { enhancedUnavailable, label, modeLines } from '@/components/agents/agent-copy'
import { mockMoment } from '@/components/agents/mock-moment'
import { SessionMock } from '@/components/agents/session-mock'
import { TerminalMock } from '@/components/agents/terminal-mock'
import type { AgentInfo } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'

export type SetupMode = 'tui' | 'acp'

function ModeCard({
  value,
  title,
  description,
  tradeOff,
  note,
  mock,
  disabled,
}: {
  value: SetupMode
  title: string
  description: string
  tradeOff: string
  note: string
  mock: ReactNode
  disabled?: boolean
}) {
  const id = useId()
  return (
    <RadioGroupPrimitive.Item
      value={value}
      disabled={disabled}
      aria-labelledby={`${id}-title`}
      aria-describedby={`${id}-description ${id}-trade-off ${id}-note`}
      className={cn(
        focusRing,
        'group flex min-w-0 cursor-pointer flex-col gap-2 rounded-panel border border-seam p-3 text-left transition-colors duration-100 motion-reduce:transition-none',
        'hover:enabled:bg-hover data-[state=checked]:border-accent data-[state=checked]:bg-hover disabled:cursor-not-allowed',
      )}
    >
      <span className="flex items-center gap-2">
        <span className="grid size-4 shrink-0 place-items-center rounded-full border border-control bg-canvas group-data-[state=checked]:border-accent group-disabled:border-seam">
          <span className="size-2 rounded-full bg-accent opacity-0 group-data-[state=checked]:opacity-100" />
        </span>
        <span id={`${id}-title`} className={cn('text-title', disabled ? 'text-muted' : 'text-text')}>
          {title}
        </span>
      </span>
      {mock}
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
    </RadioGroupPrimitive.Item>
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
        <ModeCard
          value="tui"
          title="Standard"
          description="Your agent's own terminal, exactly as on your machine."
          tradeOff="No structured view; the agent acts without asking, and anything it asks is answered in the terminal."
          note="Works with every agent."
          mock={<TerminalMock items={mockMoment} />}
        />
        <ModeCard
          value="acp"
          title="Enhanced"
          description="Aether reads what your agent is doing instead of only showing its terminal: messages, tool activity, file changes, approvals and progress appear as native controls."
          tradeOff="Runs through an adapter, not the agent's own screen; some agent-specific commands and screens are missing; starts a few seconds slower."
          note={unavailable ?? 'It asks before risky actions by default; you can change that in the run.'}
          mock={<SessionMock items={mockMoment} />}
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
