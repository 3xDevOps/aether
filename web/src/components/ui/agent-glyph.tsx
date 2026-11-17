import {
  Antigravity,
  Bot,
  Claude,
  Cursor,
  Gemini,
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
// text colour. Google's guidelines forbid altering its icons, so in colour
// Gemini CLI and Antigravity CLI are Google's full-colour files, unchanged.
const agents: Record<string, { icon: LucideIcon; colour: string } | { icon: LucideIcon; image: string }> = {
  claude: { icon: Claude, colour: 'text-agent-claude' },
  codex: { icon: OpenAI, colour: 'text-text' },
  pi: { icon: Pi, colour: 'text-agent-pi' },
  omp: { icon: OhMyPi, colour: 'text-agent-omp' },
  opencode: { icon: OpenCode, colour: 'text-text' },
  antigravity: { icon: Antigravity, image: '/agents/antigravity-icon__full-color.svg' },
  copilot: { icon: GitHub, colour: 'text-text' },
  gemini: { icon: Gemini, image: '/agents/gemini-cli-icon_full-color.svg' },
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
  if (colored && known && 'image' in known) {
    return <img data-slot="agent-glyph" aria-hidden alt="" src={known.image} className={cn('size-3.5 shrink-0', className)} />
  }
  const Icon = known?.icon ?? Bot
  return (
    <Icon
      data-slot="agent-glyph"
      aria-hidden
      className={cn('size-3.5 shrink-0', colored && known && 'colour' in known ? known.colour : 'text-muted', className)}
    />
  )
}
