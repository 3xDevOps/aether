import { RadioGroup as RadioGroupPrimitive } from 'radix-ui'
import { agentLabel } from '@/components/launch/modes'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Label } from '@/components/ui/label'
import type { AgentInfo } from '@/lib/types'
import { cn, focusRingInset } from '@/lib/utils'

export function launchable(agent: AgentInfo): boolean {
  return agent.installed === true && agent.login_missing !== true && agent.own_account_only !== true && !agent.unavailable
}

/** Never listed by agent.list; launches only where the deployment pinned it with --harness-definitions. */
export const customAgent = 'custom'

function rank(agent: AgentInfo): number {
  if (launchable(agent)) return 0
  return agent.installed ? 1 : 2
}

function status(agent: AgentInfo): string {
  if (!agent.installed) return 'Not installed'
  if (agent.login_missing) return 'Not logged in'
  if (agent.own_account_only) return 'Your account only'
  if (agent.unavailable) return 'Unavailable'
  if (agent.source !== 'shipped' || agent.login_found === undefined) return ''
  return agent.login_found ? 'Login found' : 'No login found'
}

export function AgentPicker({
  label,
  agents,
  value,
  onChange,
  onSetUp,
  disabled = false,
}: {
  label: string
  agents: AgentInfo[]
  value: string
  onChange: (name: string) => void
  onSetUp: () => void
  disabled?: boolean
}) {
  const sorted = [...agents].sort((a, b) => rank(a) - rank(b))
  const rows = [
    ...sorted.map((agent) => ({ name: agent.name, glyph: agent.glyph ?? agent.name, label: agentLabel(agent, agent.name), status: status(agent), enabled: launchable(agent), setUp: !agent.installed })),
    { name: customAgent, glyph: customAgent, label: customAgent, status: 'Server-defined', enabled: true, setUp: false },
  ]
  return (
    <div className="flex flex-col gap-1">
      <Label asChild>
        <p>{label}</p>
      </Label>
      <RadioGroupPrimitive.Root aria-label={label} value={value} onValueChange={onChange} disabled={disabled} className="flex flex-col">
        {rows.map((row) => {
          const selected = row.name === value
          return (
            <div
              key={row.name}
              className={cn('flex h-8 min-w-0 items-center rounded-control coarse:h-11', selected ? 'bg-selection' : row.enabled && 'hover:bg-hover-chrome')}
            >
              <RadioGroupPrimitive.Item
                value={row.name}
                disabled={!row.enabled}
                className={cn(focusRingInset, 'flex h-full min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-control px-2 text-left disabled:cursor-not-allowed')}
              >
                <AgentGlyph agent={row.glyph} colored={row.enabled} className="size-4" />
                <span className={cn('min-w-0 truncate text-ui', row.enabled ? 'text-text' : 'text-muted')}>{row.label}</span>
                <span className={cn('ml-auto shrink-0 text-ui-sm', selected ? 'text-text' : 'text-muted')}>{row.status}</span>
              </RadioGroupPrimitive.Item>
              {row.setUp && (
                <Button type="button" variant="link" size="sm" className="mr-2 ml-1" onClick={onSetUp} aria-label={`Set up ${row.label}`}>
                  Set up
                </Button>
              )}
            </div>
          )
        })}
      </RadioGroupPrimitive.Root>
    </div>
  )
}

export function RefusalNotes({ agents, accountName, accountField }: { agents: AgentInfo[]; accountName: string; accountField: string }) {
  const loggedOut = agents.filter((agent) => agent.installed && agent.login_missing)
  const ownOnly = agents.filter((agent) => agent.installed && agent.own_account_only)
  const unavailable = agents.filter((agent) => agent.installed && agent.unavailable)
  if (!loggedOut.length && !ownOnly.length && !unavailable.length) return null
  const names = (list: AgentInfo[]) => list.map((agent) => agent.name).join(', ')
  return (
    <Callout tone="needs-you" role="status">
      {loggedOut.length > 0 && (
        <p>
          {accountName} is not logged in to {names(loggedOut)}, so {loggedOut.length === 1 ? 'it' : 'they'} cannot launch on this account. {accountName} logs in from the terminal dock on their own Board; then open this dialog again.
        </p>
      )}
      {ownOnly.length > 0 && (
        <p>
          Your own agent definitions run only on your own account: {names(ownOnly)}. To launch one, choose your own account, marked (you), under {accountField}.
        </p>
      )}
      {unavailable.map((agent) => (
        <p key={agent.name}>
          {agent.name} cannot launch on this account: {agent.unavailable}
        </p>
      ))}
    </Callout>
  )
}
