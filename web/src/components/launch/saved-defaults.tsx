import { useState } from 'react'
import { automaticFallback, type Fallback, fallbackParams } from '@/components/launch/launch-options'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { errorSentence } from '@/lib/format'
import type { LaunchDefaultIssue, Member, SavedLaunchDefaults } from '@/lib/types'

/** The dialog's fallback for a saved policy; an ordered list keeps only accounts the member may launch on now. */
export function fallbackFromSaved(saved: SavedLaunchDefaults['fallback'], accounts: Member[]): Fallback {
  if (!saved || saved.policy === 'automatic') return automaticFallback
  if (saved.policy === 'off') return { off: true, order: null, excluded: [] }
  const order = (saved.order ?? []).filter((id) => accounts.some((member) => member.id === id))
  return { off: false, order, excluded: accounts.map((member) => member.id).filter((id) => !order.includes(id)) }
}

/** The policy a launch with `fallback` sends; `skip` is the account the Run tab leaves out of its list, as the launch does. */
export function fallbackToSaved(fallback: Fallback, accounts: Member[], skip: string): NonNullable<SavedLaunchDefaults['fallback']> {
  const params = fallbackParams(fallback, accounts, skip)
  if (params.no_fallback) return { policy: 'off' }
  if (params.fallback_account_member_ids) return { policy: 'ordered', order: params.fallback_account_member_ids }
  return { policy: 'automatic' }
}

export function issueField(field: string): string {
  if (field === 'agent') return 'Agent'
  if (field === 'account_member_id') return 'Account'
  if (field === 'fallback.order') return 'Fallback account'
  const [, name, part] = field.split('.')
  return part ? `${name} ${part}` : name
}

export function SavedIssues({ issues, title }: { issues: LaunchDefaultIssue[]; title: string }) {
  if (!issues.length) return null
  return (
    <Callout tone="needs-you" role="status" title={title}>
      <ul className="flex flex-col gap-1">
        {issues.map((issue) => (
          <li key={`${issue.field}:${issue.value}`}>
            {issueField(issue.field)} <code className="font-code">{issue.value}</code>: {issue.reason}
          </li>
        ))}
      </ul>
    </Callout>
  )
}

/** Saves the account and fallback chosen here, which `choice` identifies; a launch never saves them on its own. */
export function SaveAccountDefault({ choice, onSave }: { choice: string; onSave: () => Promise<unknown> }) {
  const [state, setState] = useState<{ busy: boolean; savedFor: string | null; error: string | null }>({ busy: false, savedFor: null, error: null })
  const save = () => {
    setState({ busy: true, savedFor: null, error: null })
    onSave().then(
      () => setState({ busy: false, savedFor: choice, error: null }),
      (err: unknown) => setState({ busy: false, savedFor: null, error: errorSentence(err) }),
    )
  }
  return (
    <div className="flex flex-col gap-1 pt-2">
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" size="sm" variant="secondary" disabled={state.busy} onClick={save}>Save as my default</Button>
        {state.savedFor === choice
          ? <span role="status" className="text-ui-sm text-muted">Saved for every device.</span>
          : <span className="text-ui-sm text-muted">Saves this account and fallback for launches on every device.</span>}
      </div>
      {state.error && <Callout tone="failed" role="alert">{state.error}</Callout>}
    </div>
  )
}
