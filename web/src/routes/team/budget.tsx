import type { ReactNode } from 'react'
import { budgetStateLabel, money } from '@/lib/format'
import { useStore } from '@/store'
import { costTotals } from '@/store/cost'
import { onlineMembers } from '@/store/presence'

/** A budget is a soft cap that never stops a run, so nothing here may suggest one was stopped. */
export function TeamSummary({ heading }: { heading?: ReactNode }) {
  const presence = useStore((s) => s.presence)
  const members = useStore((s) => s.members)
  const budgets = useStore((s) => s.budgets)
  const inboxError = useStore((s) => s.inboxError)
  const online = onlineMembers(presence)
  const parts: string[] = []
  if (online.length > 0) parts.push(`${online.length} online`)
  if (Object.keys(budgets).length > 0) {
    const totals = costTotals(budgets)
    const spend = totals.costUSD === 0
      ? totals.advisory ? 'no spend reported' : 'no spend yet'
      : `${totals.advisory ? 'at least ' : ''}${money.format(totals.costUSD)} spent`
    parts.push(totals.state === 'ok' ? spend : `${spend}, ${budgetStateLabel[totals.state]}`)
  }
  if (parts.length === 0 && !inboxError) return null
  const names = online.map((id) => members[id]?.display_name ?? id)
  return (
    <div className="pb-1 text-ui-sm text-muted">
      {heading}
      {parts.length > 0 && <p className="px-2" title={names.length ? `Online: ${names.join(', ')}` : undefined}>{parts.join(' · ')}</p>}
      {inboxError && <p role="alert" className="break-words px-2 text-state-failed">{inboxError}</p>}
    </div>
  )
}
