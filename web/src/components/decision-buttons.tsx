import { useState } from 'react'
import { RequestAction } from '@/components/ui/request-choice'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

export function DecisionButtons({ decide }: { decide: (approve: boolean) => Promise<unknown> }) {
  const [pending, setPending] = useState<{ approve: boolean } | null>(null)
  const late = usePastDeadline(pending !== null)
  const press = (approve: boolean) => {
    const mine = { approve }
    setPending(mine)
    void decide(approve).finally(() => setPending((current) => (current === mine ? null : current)))
  }
  const label = (approve: boolean) => pending?.approve !== approve
    ? (approve ? 'Approve' : 'Deny')
    : late ? stillWaiting : approve ? 'Approving…' : 'Denying…'
  return (
    <>
      <RequestAction label={label(false)} disabled={pending !== null} onClick={() => press(false)} />
      <RequestAction label={label(true)} variant="primary" disabled={pending !== null} onClick={() => press(true)} />
    </>
  )
}
