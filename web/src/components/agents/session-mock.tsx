import { FileText, Terminal } from '@/components/icons'
import type { SessionItem, SessionToolCall } from '@/lib/session-types'

function WorkLine({ tool }: { tool: SessionToolCall }) {
  const Icon = tool.tool_kind === 'execute' ? Terminal : FileText
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
      <Icon className="size-3 shrink-0" />
      <span className="truncate">{tool.title}</span>
    </span>
  )
}

export function SessionMock({ items }: { items: SessionItem[] }) {
  return (
    <span aria-hidden className="flex min-h-40 flex-col gap-2 overflow-hidden rounded-control border border-seam bg-canvas p-3">
      {items.map((item) => {
        const { message, tool_call: tool } = item
        if (message?.role === 'user') {
          return <span key={item.seq} className="block rounded-panel bg-chrome px-2 py-1 text-ui-sm text-text">{message.text}</span>
        }
        if (message?.role === 'assistant') {
          return <span key={item.seq} className="block text-ui-sm text-text">{message.text}</span>
        }
        if (tool) return <WorkLine key={item.seq} tool={tool} />
        return null
      })}
    </span>
  )
}
