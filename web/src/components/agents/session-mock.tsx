import { FileText, Terminal } from '@/components/icons'
import { buttonVariants } from '@/components/ui/button'
import type { SessionItem, SessionRequest, SessionToolCall } from '@/lib/session-types'

function WorkLine({ tool }: { tool: SessionToolCall }) {
  const Icon = tool.tool_kind === 'execute' ? Terminal : FileText
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
      <Icon className="size-3 shrink-0" />
      <span className="truncate">{tool.title}</span>
    </span>
  )
}

function RequestMock({ request }: { request: SessionRequest }) {
  return (
    <span className="flex flex-col gap-1.5 rounded-panel bg-state-needs-you-soft p-2">
      <span className="flex min-w-0 items-baseline gap-1.5 text-ui-sm">
        <span className="shrink-0 font-medium text-state-needs-you">Permission</span>
        <span className="truncate rounded-control bg-chrome px-1 font-code text-text">{request.title}</span>
      </span>
      <span className="flex gap-1.5">
        {request.options?.map((option, index) => (
          <span key={option.id} className={buttonVariants({ variant: index === 0 ? 'primary' : 'secondary', size: 'sm' })}>
            {option.name}
          </span>
        ))}
      </span>
    </span>
  )
}

export function SessionMock({ items }: { items: SessionItem[] }) {
  const asked = new Set(items.flatMap((item) => (item.request?.status === 'pending' && item.request.tool_call_id ? [item.request.tool_call_id] : [])))
  return (
    <span aria-hidden className="flex min-h-40 flex-col gap-2 overflow-hidden rounded-control border border-seam bg-canvas p-3">
      {items.map((item) => {
        const { message, tool_call: tool, request } = item
        if (message?.role === 'user') {
          return <span key={item.seq} className="block rounded-panel bg-chrome px-2 py-1 text-ui-sm text-text">{message.text}</span>
        }
        if (message?.role === 'assistant') {
          return <span key={item.seq} className="block text-ui-sm text-text">{message.text}</span>
        }
        if (tool && !asked.has(tool.id)) return <WorkLine key={item.seq} tool={tool} />
        if (request?.status === 'pending') return <RequestMock key={item.seq} request={request} />
        return null
      })}
    </span>
  )
}
