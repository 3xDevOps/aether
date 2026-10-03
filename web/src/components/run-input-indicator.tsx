import { MessageCircleQuestion } from 'lucide-react'
import { Chip, Tooltip } from '@/components/ui/heroui'
import type { Run } from '@/lib/types'
import { focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import { useRunInput } from '@/store/hooks'

/** Existing answer surfaces only; no prompt contents or answers travel in this metadata. */
export function RunInputIndicator({ run, compact = false }: { run: Run; compact?: boolean }) {
  const input = useRunInput(run)
  const navigate = useStore((s) => s.navigate)
  if (input.count === 0) return null
  const description = `Needs input: ${input.summary}`
  if (compact) {
    return (
      <span role="img" aria-label={description} title={description} className="inline-flex shrink-0 items-center gap-0.5 text-state-needs-attention">
        <MessageCircleQuestion className="size-3.5" aria-hidden />
        {input.count > 1 && <span className="text-[10px] tabular-nums">{input.count}</span>}
      </span>
    )
  }
  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            aria-label={description}
            onClick={() => navigate(input.destination, input.destination === 'terminal' ? { runId: run.id } : {})}
            className={`${focusRing} inline-flex shrink-0 items-center rounded-sm coarse:min-h-11`}
          >
            <Chip color="warning" variant="soft" size="sm">
              <MessageCircleQuestion className="size-3" aria-hidden />
              <Chip.Label>Needs input{input.count > 1 ? ` ${input.count}` : ''}</Chip.Label>
            </Chip>
          </button>
        )}
      />
      <Tooltip.Content>{description}. Open {input.destination === 'terminal' ? 'Terminal' : 'Approvals'} to respond.</Tooltip.Content>
    </Tooltip>
  )
}
