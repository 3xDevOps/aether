import { stateDotClass, stateLabel, type PresentationState } from '@/lib/status'
import { cn } from '@/lib/utils'

export function StateDot({
  state,
  className,
}: {
  state: PresentationState
  className?: string
}) {
  return (
    <span
      role="img"
      aria-label={stateLabel[state]}
      title={stateLabel[state]}
      className={cn('size-2 shrink-0 rounded-full', stateDotClass[state], className)}
    />
  )
}
