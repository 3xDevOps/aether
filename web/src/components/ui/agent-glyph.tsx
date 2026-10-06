import { Bot, Boxes, type LucideIcon, Pi, Sparkles, SquarePi, SquareTerminal, Wrench } from '@/components/icons'
import { cn } from '@/lib/utils'

// Keys are internal/harness registry names.
const agents: Record<string, { icon: LucideIcon; colour: string }> = {
  claude: { icon: Sparkles, colour: 'text-agent-claude' },
  codex: { icon: SquareTerminal, colour: 'text-agent-codex' },
  pi: { icon: Pi, colour: 'text-agent-pi' },
  omp: { icon: SquarePi, colour: 'text-agent-omp' },
  opencode: { icon: Boxes, colour: 'text-agent-opencode' },
  custom: { icon: Wrench, colour: 'text-muted' },
}

export function AgentGlyph({
  agent,
  colored = false,
  className,
}: {
  agent: string
  colored?: boolean
  className?: string
}) {
  const known = agents[agent]
  const Icon = known?.icon ?? Bot
  return (
    <Icon
      data-slot="agent-glyph"
      aria-hidden
      className={cn('size-3.5 shrink-0', colored && known ? known.colour : 'text-muted', className)}
    />
  )
}
