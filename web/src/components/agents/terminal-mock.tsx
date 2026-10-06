import type { SessionItem } from '@/lib/session-types'

type Line = { key: string; text: string; tone: 'text' | 'muted' | 'prompt' | 'tool' }

const tones: Record<Line['tone'], string> = {
  text: 'text-text',
  muted: 'text-muted',
  prompt: 'text-accent',
  tool: 'text-state-done',
}

function lines(items: SessionItem[]): Line[] {
  return items.flatMap((item): Line[] => {
    const { message, tool_call: tool } = item
    if (message?.role === 'user') return [{ key: `${item.seq}`, text: `> ${message.text}`, tone: 'prompt' }]
    if (message?.role === 'assistant') return [{ key: `${item.seq}`, text: `  ${message.text}`, tone: 'text' }]
    if (tool) {
      return [
        { key: `${item.seq}`, text: `● ${tool.title}`, tone: 'tool' },
        { key: `${item.seq}-out`, text: `  └ ${tool.status === 'completed' ? tool.output ?? 'Done' : 'Running…'}`, tone: 'muted' },
      ]
    }
    return []
  })
}

/** Spans only: it sits inside the comparison card's button. */
export function TerminalMock({ items }: { items: SessionItem[] }) {
  return (
    <span aria-hidden className="flex min-h-40 flex-col gap-0.5 overflow-hidden rounded-control border border-seam bg-canvas p-3 font-code text-ui-xs">
      {lines(items).map((line) => (
        <span key={line.key} className={`block whitespace-pre-wrap break-words ${tones[line.tone]}`}>
          {line.text}
        </span>
      ))}
      <span className="mt-0.5 block h-3.5 w-1.5 bg-chrome" />
    </span>
  )
}
