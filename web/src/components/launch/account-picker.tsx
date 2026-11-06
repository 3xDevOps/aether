import { useEffect, useState } from 'react'
import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import { customAgent, launchable } from '@/components/launch/agent-picker'
import { Label } from '@/components/ui/label'
import { api, type Api } from '@/lib/api'
import type { AgentInfo, Member, UsageProvider } from '@/lib/types'
import { cn, focusRingInset } from '@/lib/utils'
import { currentWindows, tone } from '@/routes/settings/usage'

/** What `agent.list` and `account.usage` said for one account; a field stays absent until its read succeeds. */
export type AccountStatus = { agents?: AgentInfo[]; usage?: UsageProvider[] }

/** Reads every account's agents and usage once per account list, so each row can say whether the chosen agent launches there. */
export function useAccountStatus(accounts: Member[], ownAccountID: string, client: Pick<Api, 'agentList' | 'accountUsage'> = api): Record<string, AccountStatus> {
  const [status, setStatus] = useState<Record<string, AccountStatus>>({})
  const ids = accounts.length > 1 ? accounts.map((member) => member.id).join(' ') : ''
  useEffect(() => {
    setStatus({})
    if (!ids) return
    let live = true
    const set = (id: string, next: AccountStatus) => {
      if (live) setStatus((current) => ({ ...current, [id]: { ...current[id], ...next } }))
    }
    for (const id of ids.split(' ')) {
      const shared = id === ownAccountID ? undefined : id
      client.agentList(shared).then((agents) => set(id, { agents }), () => {})
      client.accountUsage(shared ? { account_member_id: shared } : {}).then((usage) => set(id, { usage: usage.providers }), () => {})
    }
    return () => { live = false }
  }, [ids, ownAccountID, client])
  return status
}

const toneText = { failed: 'text-state-failed', 'needs-you': 'text-state-needs-you', done: 'text-muted' } as const

/** Why `harness` cannot launch on the account, or its highest current usage window; null when neither is known. */
function readiness(status: AccountStatus | undefined, harness: string, now: number): { text: string; className: string } | null {
  if (!status || !harness || harness === customAgent) return null
  const agent = status.agents?.find((item) => item.name === harness)
  if (status.agents && !agent?.installed) return { text: 'Not installed', className: 'text-muted' }
  if (agent?.login_missing) return { text: 'Not logged in', className: 'text-state-needs-you' }
  if (agent?.own_account_only) return { text: 'Your account only', className: 'text-muted' }
  if (agent?.unavailable) return { text: 'Unavailable', className: 'text-state-failed' }
  const windows = currentWindows(status.usage?.find((provider) => provider.provider === harness), now)
  if (windows.length) {
    const used = Math.max(...windows.map((window) => window.used_percent))
    return { text: `${Math.round(used)}% used`, className: toneText[tone(used)] }
  }
  if (!agent || !launchable(agent)) return null
  return { text: agent.login_found === false ? 'No login found' : 'Ready', className: 'text-muted' }
}

/** The account a launch runs on: the caller's own and each one shared with them, with the chosen agent's readiness on it. */
export function AccountPicker({ accounts, ownAccountID, value, onChange, harness, status, disabled = false }: {
  accounts: Member[]
  ownAccountID: string
  value: string
  onChange: (account: string) => void
  harness: string
  status: Record<string, AccountStatus>
  disabled?: boolean
}) {
  const now = Date.now()
  const chosen = accounts.find((member) => member.id === value)
  const shared = value !== '' && value !== ownAccountID
  return (
    <div className="flex flex-col gap-1">
      <Label asChild>
        <p>Account</p>
      </Label>
      <RadioGroupPrimitive.Root aria-label="Account" value={value} onValueChange={onChange} disabled={disabled} className="flex flex-col">
        {accounts.map((member) => {
          const selected = member.id === value
          const ready = readiness(status[member.id], harness, now)
          return (
            <div key={member.id} className={cn('flex h-8 min-w-0 items-center rounded-control coarse:h-11', selected ? 'bg-selection' : 'hover:bg-hover-chrome')}>
              <RadioGroupPrimitive.Item
                value={member.id}
                className={cn(focusRingInset, 'flex h-full min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-control px-2 text-left disabled:cursor-not-allowed')}
              >
                <span className="min-w-0 truncate text-ui text-text">{member.display_name}</span>
                <span className="shrink-0 text-ui-sm text-muted">{member.id === ownAccountID ? 'Your own' : 'Shared'}</span>
                {ready && <span className={cn('ml-auto shrink-0 text-ui-sm tabular-nums', ready.className)}>{ready.text}</span>}
              </RadioGroupPrimitive.Item>
            </div>
          )
        })}
      </RadioGroupPrimitive.Root>
      {shared && (
        <p className="text-ui-sm text-muted">
          Uses {chosen?.display_name ?? value}&apos;s agent login and vendor quota in your own environment, with your GitHub login. You remain its owner and actor.
        </p>
      )}
    </div>
  )
}
