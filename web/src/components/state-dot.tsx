import { stateDotClass, stateLabel, type PresentationState } from '@/lib/status'
import { cn } from '@/lib/utils'

/**
 * `decorative` drops the label and the tooltip, for the places that already
 * print the state in words beside the mark; a reader hears it once.
 */
interface MarkProps {
  state: PresentationState
  className?: string
  decorative?: boolean
}

function labelling(state: PresentationState, decorative: boolean) {
  return decorative
    ? { 'aria-hidden': true }
    : { role: 'img', 'aria-label': stateLabel[state], title: stateLabel[state] }
}

export function StateDot({ state, className, decorative = false }: MarkProps) {
  return (
    <span
      {...labelling(state, decorative)}
      className={cn('size-2 shrink-0 rounded-full', stateDotClass[state], className)}
    />
  )
}

/** Three bouncing dots: the working state, in motion. The animation and its
 * reduced-motion fallback live in `index.css` next to the steering signal. */
function WorkingDots({ state, className, decorative = false }: MarkProps) {
  return (
    <span {...labelling(state, decorative)} className={cn('working-dots', className)}>
      <span aria-hidden />
      <span aria-hidden />
      <span aria-hidden />
    </span>
  )
}

/** The state mark a run surface shows: a working run bounces, every other
 * state is the static dot. */
export function StateIndicator(props: MarkProps) {
  return props.state === 'working' ? <WorkingDots {...props} /> : <StateDot {...props} />
}
