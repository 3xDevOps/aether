import {
  AntigravityInitial,
  Bot,
  Claude,
  Cursor,
  GeminiInitial,
  GitHub,
  type LucideIcon,
  OhMyPi,
  OpenAI,
  OpenCode,
  Pi,
  Wrench,
} from '@/components/icons'
import { cn } from '@/lib/utils'

// Keys are internal/harness registry names. OpenAI, OpenCode and GitHub
// forbid recolouring their marks, so Codex, OpenCode and Copilot keep the
// text colour.
const agents: Record<string, { icon: LucideIcon; colour: string }> = {
  claude: { icon: Claude, colour: 'text-agent-claude' },
  codex: { icon: OpenAI, colour: 'text-text' },
  pi: { icon: Pi, colour: 'text-agent-pi' },
  omp: { icon: OhMyPi, colour: 'text-agent-omp' },
  opencode: { icon: OpenCode, colour: 'text-text' },
  antigravity: { icon: AntigravityInitial, colour: 'text-agent-antigravity' },
  copilot: { icon: GitHub, colour: 'text-text' },
  gemini: { icon: GeminiInitial, colour: 'text-agent-gemini' },
  cursor: { icon: Cursor, colour: 'text-agent-cursor' },
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
