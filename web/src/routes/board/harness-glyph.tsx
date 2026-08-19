import { Bot, Boxes, PenLine, Sparkles, SquareTerminal, Wrench } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

// Who is running, never merged with the state dot. Names are the harness
// registry's (internal/harness); anything else falls back to the generic bot.
const glyphs: Record<string, LucideIcon> = {
  claude: Sparkles,
  codex: SquareTerminal,
  aider: PenLine,
  opencode: Boxes,
  custom: Wrench,
}

export function HarnessGlyph({ harness, mode }: { harness: string; mode: string }) {
  const Icon = glyphs[harness] ?? Bot
  return (
    <span className="flex shrink-0 items-center gap-1" title={`${harness} (${mode})`}>
      <Icon className="size-3.5" aria-hidden />
      {harness}
    </span>
  )
}
