import { useEffect, useState } from 'react'
import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import { customAgent, needsLogin } from '@/components/launch/agent-picker'
import { Label } from '@/components/ui/label'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentInfo, Member, UsageProvider } from '@/lib/types'
import { cn, focusRingInset } from '@/lib/utils'
import { currentWindows, tone } from '@/routes/settings/usage'

export type AccountStatus = { agents?: AgentInfo[]; agentsError?: string; usage?: UsageProvider[]; usageError?: string }

/** The connector's bound on account reads in flight, so they leave the gateway's connections to the launch itself. */
const statusReads = 4

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
    const reads = ids.split(' ').flatMap((id) => {
      const shared = id === ownAccountID ? undefined : id
      return [
        () => client.agentList(shared).then((agents) => set(id, { agents }), (err: unknown) => set(id, { agentsError: message(err) })),
        () => client.accountUsage(shared ? { account_member_id: shared } : {}).then((usage) => set(id, { usage: usage.providers }), (err: unknown) => set(id, { usageError: message(err) })),
      ]
    })
    const next = (): Promise<void> => {
      const read = live ? reads.shift() : undefined
      return read ? read().then(next) : Promise.resolve()
    }
    for (let i = 0; i < statusReads; i++) void next()
    return () => { live = false }
  }, [ids, ownAccountID, client])
  return status
}

/** `refused`: the server refuses a launch there, so usage fallback skips it too. */
export type Readiness = { text: string; className: string; title?: string; refused?: boolean; signIn?: boolean }

export function readiness(status: AccountStatus | undefined, harness: string, owner: string | undefined, now: number): Readiness | null {
  if (!status || !harness || harness === customAgent) return null
  if (status.agentsError) return { text: 'Not checked', className: 'text-state-failed', title: status.agentsError }
  if (!status.agents) return null
  const agent = status.agents.find((item) => item.name === harness)
  if (!agent?.installed) return { text: 'Not installed', className: 'text-muted', refused: true }
  if (agent.login_missing) return { text: needsLogin(owner), title: needsLogin(owner, false), className: 'text-state-needs-you', refused: true }
  if (agent.own_account_only) return { text: 'Your account only', className: 'text-muted', refused: true }
  if (agent.unavailable) return { text: 'Unavailable', className: 'text-state-failed', title: agent.unavailable, refused: true }
  if (agent.login_problem) return { text: 'Signed out', className: 'text-state-needs-you', title: agent.login_problem }
  const provider = status.usage?.find((item) => item.provider === harness)
  const windows = currentWindows(provider, now)
  if (windows.length) {
    const fullest = Math.max(...windows.map((window) => window.used_percent))
    const used = Math.round(fullest)
    const meter = tone(fullest)
    const className = meter === 'done' ? 'text-text' : meter === 'failed' ? 'text-state-failed' : 'text-state-needs-you'
    return provider?.status === 'stale'
      ? { text: `${used}% used · stale`, className, title: provider.error }
      : { text: `${used}% used`, className }
  }
  const usageError = status.usageError ? `Usage not read: ${status.usageError}` : undefined
  if (agent.login_found === false) return { text: 'Sign in after launch', className: 'text-muted', title: usageError, signIn: true }
  return { text: 'Ready', className: 'text-text', title: usageError }
}

const noLoginFallback = 'A launch may still fall back here, last, and the agent asks you to sign in. A running session moves here only if an API key signs the agent in.'

export function asFallback(ready: Readiness | null): Readiness | null {
  if (ready?.refused) return { ...ready, text: `Skipped: ${ready.text.charAt(0).toLowerCase()}${ready.text.slice(1)}`, className: 'text-muted' }
  // Launch-time fallback may still pick it, last; a running session's move
  // (chooseMove) skips it unless an API key signs the agent in.
  if (ready?.signIn) return { ...ready, text: 'No saved login', title: noLoginFallback }
  return ready
}

export function accessLabel(member: Member, ownAccountID: string): string {
  return member.id === ownAccountID ? 'You' : 'Shared'
}

export function ReadinessText({ ready, className }: { ready: Readiness | null; className?: string }) {
  if (!ready) return null
  return <span title={ready.title ?? ready.text} className={cn('ml-auto max-w-[60%] shrink-0 truncate text-ui-sm tabular-nums', ready.className, className)}>{ready.text}</span>
}

export function AccountPicker({ accounts, ownAccountID, value, onChange, agentName, status, disabled = false }: {
  accounts: Member[]
  ownAccountID: string
  value: string
  onChange: (account: string) => void
  agentName: string
  status: (member: Member) => Readiness | null
  disabled?: boolean
}) {
  const chosen = accounts.find((member) => member.id === value)
  const shared = value !== '' && value !== ownAccountID
  return (
    <div className="flex flex-col gap-1">
      <Label asChild>
        <p>Account</p>
      </Label>
      <RadioGroupPrimitive.Root aria-label="Account" value={value} onValueChange={onChange} disabled={disabled} className="flex flex-col">
        {accounts.map((member) => (
          <div key={member.id} className={cn('flex h-8 min-w-0 items-center rounded-control coarse:h-11', member.id === value ? 'bg-selection' : 'hover:bg-hover-chrome')}>
            <RadioGroupPrimitive.Item
              value={member.id}
              className={cn(focusRingInset, 'flex h-full min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-control px-2 text-left disabled:cursor-not-allowed')}
            >
              <span title={member.display_name} className="min-w-0 truncate text-ui text-text">{member.display_name}</span>
              <span className="shrink-0 text-ui-sm text-muted">{accessLabel(member, ownAccountID)}</span>
              <ReadinessText ready={status(member)} />
            </RadioGroupPrimitive.Item>
          </div>
        ))}
      </RadioGroupPrimitive.Root>
      {shared && (
        <p className="text-ui-sm text-muted">
          Uses {chosen?.display_name ?? value}&apos;s {agentName ? `${agentName} ` : ''}login and quota. Runs in your environment with your GitHub login; the session stays yours.
        </p>
      )}
    </div>
  )
}
