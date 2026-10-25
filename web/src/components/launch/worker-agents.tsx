import { launchable } from '@/components/launch/agent-picker'
import { agentLabel, modeLabel, modes, type Refusal, swarmRefusal } from '@/components/launch/modes'
import { ModelPicker } from '@/components/launch/model-picker'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import type { AgentInfo, LaunchMode, Member } from '@/lib/types'
import { cn } from '@/lib/utils'

export type WorkerChoice = { account_member_id: string; harness: string; model?: string }

export function sameWorker(a: WorkerChoice, b: WorkerChoice): boolean {
  return a.account_member_id === b.account_member_id && a.harness === b.harness && (a.model ?? '') === (b.model ?? '')
}

/** The mode a worker of `agent` runs in: `chosen` if it can, else the first of Enhanced, Standard and Background it can. */
export function workerMode(agent: AgentInfo | undefined, name: string, chosen: LaunchMode): LaunchMode {
  return ([chosen, 'acp', 'tui'] as const).find((mode) => !swarmRefusal(agent, name, mode)) ?? 'headless'
}

/** The modes no chosen worker can run in, each with every worker's reason. */
export function workerRefusals(choices: WorkerChoice[], agentsByAccount: Record<string, AgentInfo[]> | null): Partial<Record<LaunchMode, Refusal>> {
  if (!choices.length) return {}
  return Object.fromEntries(modes.flatMap(({ value }) => {
    const each = choices.map((choice) => swarmRefusal(agentsByAccount?.[choice.account_member_id]?.find((item) => item.name === choice.harness), choice.harness, value))
    if (each.some((refusal) => refusal === null)) return []
    const refused = each as Refusal[]
    return [[value, { reason: [...new Set(refused.map((refusal) => refusal.reason))].join(' '), short: refused[0].short, setUp: refused.some((refusal) => refusal.setUp) }]]
  }))
}

function refusal(member: Member, agent: AgentInfo): string | null {
  if (agent.login_missing) return `${member.display_name} is not logged in to ${agentLabel(agent, agent.name)}`
  if (agent.own_account_only) return 'your own definition runs only on your own account'
  return agent.unavailable ?? null
}

export function WorkerAgents({
  accounts,
  agentsByAccount,
  error,
  choices,
  mode,
  ownAccountID,
  onToggle,
  onModel,
}: {
  accounts: Member[]
  agentsByAccount: Record<string, AgentInfo[]> | null
  error: string | null
  choices: WorkerChoice[]
  mode: LaunchMode
  ownAccountID: string
  onToggle: (choice: WorkerChoice, on: boolean) => void
  onModel: (choice: WorkerChoice, model: string) => void
}) {
  const rows = accounts.flatMap((member) => (agentsByAccount?.[member.id] ?? []).map((agent) => ({ member, agent })))
  return (
    <div className="flex flex-col gap-1">
      <Label asChild>
        <p>Agents for workers</p>
      </Label>
      {error ? (
        <Callout tone="failed" role="alert">Listing agents for workers failed: {error}</Callout>
      ) : agentsByAccount === null ? (
        <p className="text-ui-sm text-muted">Loading installed agents…</p>
      ) : rows.length === 0 ? (
        <p className="text-ui-sm text-muted">No agent is installed for workers.</p>
      ) : (
        <ul className="flex flex-col">
          {rows.map(({ member, agent }) => {
            const picked = choices.find((item) => item.account_member_id === member.id && item.harness === agent.name)
            const checked = picked !== undefined
            const refused = refusal(member, agent)
            const runs = checked ? workerMode(agent, agent.name, mode) : mode
            const fallback = runs === mode ? null : swarmRefusal(agent, agent.name, mode)
            const name = accounts.length > 1 ? `${member.display_name} · ${agentLabel(agent, agent.name)}` : agentLabel(agent, agent.name)
            return (
              <li key={`${member.id}:${agent.name}`}>
                <label className={cn('flex min-h-7 min-w-0 items-center gap-2 rounded-control px-2 text-ui coarse:min-h-11', refused ? 'cursor-not-allowed text-muted' : 'cursor-pointer hover:bg-hover-chrome')}>
                  <Checkbox checked={checked} disabled={!launchable(agent)} onCheckedChange={(on) => onToggle(picked ?? { account_member_id: member.id, harness: agent.name }, on === true)} />
                  <AgentGlyph agent={agent.glyph ?? agent.name} colored={!refused} className="size-4" />
                  <span className="shrink-0">{name}</span>
                  {(refused || fallback) && (
                    <span className="ml-auto min-w-0 py-1 text-right text-ui-sm break-words text-muted">
                      {refused ?? `Runs ${modeLabel(runs)}: ${fallback?.short}`}
                    </span>
                  )}
                </label>
                {picked && (
                  <div className="pr-2 pb-1 pl-8">
                    <ModelPicker
                      aria-label={`Model for ${name}`}
                      harness={agent.name}
                      accountMemberID={member.id === ownAccountID ? undefined : member.id}
                      value={picked.model ?? ''}
                      onChange={(model) => onModel(picked, model)}
                    />
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
