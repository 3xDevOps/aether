import { AgentGlyph } from '@/components/ui/agent-glyph'

export function HarnessGlyph({ harness, mode }: { harness: string; mode: string }) {
  return (
    <span className="flex min-w-0 max-w-full items-center gap-1 text-ui-sm" title={`${harness} (${mode})`}>
      <AgentGlyph agent={harness} />
      <span className="min-w-0 truncate">{harness}</span>
      <span className="shrink-0 text-muted">({mode})</span>
    </span>
  )
}
