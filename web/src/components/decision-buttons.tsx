import { useState } from 'react'
import { RequestAction, StaleReason } from '@/components/ui/request-choice'
import { useStale } from '@/store/hooks'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

export function DecisionButtons({ decide }: { decide: (approve: boolean) => Promise<unknown> }) {
  const [pending, setPending] = useState<{ approve: boolean } | null>(null)
  // A painted snapshot may be minutes old: the request could be answered already.
  const stale = useStale()
  const late = usePastDeadline(pending !== null)
  const press = (approve: boolean) => {
    const mine = { approve }
    setPending(mine)
    void decide(approve).finally(() => setPending((current) => (current === mine ? null : current)))
  }
  const label = (approve: boolean) => stale ? (approve ? 'Approve' : 'Deny') : pending?.approve !== approve
    ? (approve ? 'Approve' : 'Deny')
    : late ? stillWaiting : approve ? 'Approving…' : 'Denying…'
  return (
    <>
      <StaleReason />
      <RequestAction label={label(false)} variant="danger" disabled={pending !== null} onClick={() => press(false)} />
      <RequestAction label={label(true)} variant="confirm" disabled={pending !== null} onClick={() => press(true)} />
    </>
  )
}
