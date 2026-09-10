import { Bot, Boxes, Sparkles, SquareTerminal, Wrench } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

// Who is running, never merged with the state dot. Names are the harness
// registry's (internal/harness); anything else falls back to the generic bot.
const glyphs: Record<string, LucideIcon> = {
  claude: Sparkles,
  codex: SquareTerminal,
  opencode: Boxes,
  custom: Wrench,
}

export function HarnessGlyph({ harness, mode }: { harness: string; mode: string }) {
  const Icon = glyphs[harness] ?? Bot
  return (
    <span
      className="flex min-w-0 max-w-full items-center gap-1 text-xs"
      title={`${harness} (${mode})`}
    >
      <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 truncate">{harness}</span>
      <span className="shrink-0 text-muted-foreground/80">({mode})</span>
    </span>
  )
}
