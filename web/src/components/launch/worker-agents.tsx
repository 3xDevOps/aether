import { launchable } from '@/components/launch/agent-picker'
import { agentLabel, modeRefusal } from '@/components/launch/modes'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import type { AgentInfo, LaunchMode, Member } from '@/lib/types'
import { cn } from '@/lib/utils'

export type WorkerChoice = { account_member_id: string; harness: string }

export function sameWorker(a: WorkerChoice, b: WorkerChoice): boolean {
  return a.account_member_id === b.account_member_id && a.harness === b.harness
}

function refusal(member: Member, agent: AgentInfo): string | null {
  if (agent.login_missing) return `${member.display_name} is not logged in to ${agent.name}`
  if (agent.own_account_only) return 'your own definition runs only on your own account'
  return agent.unavailable ?? null
}

export function WorkerAgents({
  accounts,
  agentsByAccount,
  choices,
  mode,
  onToggle,
}: {
  accounts: Member[]
  agentsByAccount: Record<string, AgentInfo[]> | null
  choices: WorkerChoice[]
  mode: LaunchMode
  onToggle: (choice: WorkerChoice, on: boolean) => void
}) {
  const rows = accounts.flatMap((member) => (agentsByAccount?.[member.id] ?? []).map((agent) => ({ member, agent })))
  return (
    <div className="flex flex-col gap-1">
      <Label asChild>
        <p>Agents for workers</p>
      </Label>
      {agentsByAccount === null ? (
        <p className="text-ui-sm text-muted">Loading installed agents…</p>
      ) : rows.length === 0 ? (
        <p className="text-ui-sm text-muted">No agent is installed for workers.</p>
      ) : (
        <ul className="flex flex-col">
          {rows.map(({ member, agent }) => {
            const choice = { account_member_id: member.id, harness: agent.name }
            const checked = choices.some((item) => sameWorker(item, choice))
            const refused = refusal(member, agent)
            const fallback = checked ? modeRefusal(agent, agent.name, mode) : null
            const name = accounts.length > 1 ? `${member.display_name} · ${agentLabel(agent, agent.name)}` : agentLabel(agent, agent.name)
            return (
              <li key={`${member.id}:${agent.name}`}>
                <label className={cn('flex min-h-7 min-w-0 items-center gap-2 rounded-control px-2 text-ui coarse:min-h-11', refused ? 'cursor-not-allowed text-muted' : 'cursor-pointer hover:bg-hover-chrome')}>
                  <Checkbox checked={checked} disabled={!launchable(agent)} onCheckedChange={(on) => onToggle(choice, on === true)} />
                  <AgentGlyph agent={agent.glyph ?? agent.name} colored={!refused} className="size-4" />
                  <span className="shrink-0">{name}</span>
                  {(refused || fallback) && (
                    <span className="ml-auto min-w-0 py-1 text-right text-ui-sm break-words text-muted">
                      {refused ?? `Runs Standard: ${fallback?.short}`}
                    </span>
                  )}
                </label>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
