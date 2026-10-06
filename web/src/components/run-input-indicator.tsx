import { MessageCircleQuestion } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import type { Run } from '@/lib/types'
import { focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import { useRunInput } from '@/store/hooks'

/** Existing answer surfaces only; no prompt contents or answers travel in this metadata. */
export function RunInputIndicator({ run, compact = false }: { run: Run; compact?: boolean }) {
  const input = useRunInput(run)
  const navigate = useStore((s) => s.navigate)
  if (input.count === 0) return null
  const description = `Requests: ${input.summary}`
  if (compact) {
    return (
      <span role="img" aria-label={description} title={description} className="inline-flex shrink-0 items-center gap-0.5 text-state-needs-attention">
        <MessageCircleQuestion className="size-3.5" aria-hidden />
        {input.count > 1 && <span className="text-[10px] tabular-nums">{input.count}</span>}
      </span>
    )
  }
  return (
    <Tooltip content={<>{description}. Open {input.destination === 'terminal' ? 'Terminal' : 'Approvals'} to respond.</>}>
      <button
        type="button"
        aria-label={description}
        onClick={() => navigate(input.destination, input.destination === 'terminal' ? { runId: run.id } : {})}
        className={`${focusRing} inline-flex shrink-0 items-center rounded-sm coarse:min-h-11`}
      >
        <Badge tone="needs-you">
          <MessageCircleQuestion className="size-3" aria-hidden />
          {input.count} {input.count === 1 ? 'request' : 'requests'}
        </Badge>
      </button>
    </Tooltip>
  )
}
