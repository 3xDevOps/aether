import { useState } from 'react'
import { Check, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

export function DecisionButtons({ decide, size = 'sm', icons = false }: {
  decide: (approve: boolean) => Promise<unknown>
  size?: 'sm' | 'md'
  icons?: boolean
}) {
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
      <Button size={size} disabled={pending !== null} onClick={() => press(true)}>{icons && <Check />}{label(true)}</Button>
      <Button size={size} variant="secondary" disabled={pending !== null} onClick={() => press(false)}>{icons && <X />}{label(false)}</Button>
    </>
  )
}
