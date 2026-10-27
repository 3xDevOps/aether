import { useMemo } from 'react'
import { modeLabel } from '@/routes/run/agent-name'
import { useStore, type RootState } from '@/store'

/** The methods whose card says what a launch starts and where. */
const launchMethods = new Set(['run.launch', 'mission.create'])

/** Whether `method` starts work, which another member's account may pay for. */
export function isLaunch(method: string): boolean {
  return launchMethods.has(method) || method === 'template.launch'
}

export const hearBack = {
  run: 'Aether notifies you here, on the desktop app and on your phone when it finishes or needs you.',
  swarm: 'The swarm’s page shows its progress; Aether notifies you when it needs you.',
}

type Params = Record<string, unknown>

function str(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}

/** The account's owner, or undefined for the member's own: an empty id launches on their own. */
function sharer(s: RootState, id: string | undefined): string | undefined {
  return !id || id === s.info?.member.id ? undefined : s.members[id]?.display_name || id
}

function agentLine(s: RootState, choice: Params): string {
  const harness = str(choice.harness) ?? ''
  const name = s.agentList?.agents.find((agent) => agent.name === harness)?.display_name ?? harness
  const mode = str(choice.mode)
  return [name, str(choice.model), mode && modeLabel[mode] ? `(${modeLabel[mode]})` : undefined].filter(Boolean).join(' ')
}

function fallbackLine(s: RootState, params: Params): string {
  if (params.no_fallback === true) return 'Off: stays on this account'
  const order = Array.isArray(params.fallback_account_member_ids) ? params.fallback_account_member_ids.filter((id): id is string => typeof id === 'string') : []
  if (!order.length) return 'Any account you may launch on, most usage left first'
  return `Then ${order.map((id) => s.members[id]?.display_name || id).join(', ')}`
}

/** The rows a launch card shows, or undefined when `method` launches nothing. */
export function launchRows(s: RootState, method: string, params: unknown): [string, string][] | undefined {
  if (!launchMethods.has(method) || params === null || typeof params !== 'object') return undefined
  const p = params as Params
  const swarm = method === 'mission.create'
  const lead = (swarm ? p.integrator : p) as Params | undefined
  const workspace = str(p.workspace_id)
  const owner = sharer(s, str(lead?.account_member_id))
  const extras = Array.isArray(p.extra_workspace_ids) ? p.extra_workspace_ids.filter((id): id is string => typeof id === 'string') : []
  const rows: [string, string][] = [
    ['Kind', swarm ? 'Swarm: an integrator plans the work for worker runs' : 'Run: one agent on one task'],
    ['Workspace', [workspace, ...extras].filter(Boolean).map((id) => s.workspaces[id!]?.name ?? id).join(', ')],
    [swarm ? 'Integrator' : 'Agent', lead ? agentLine(s, lead) : ''],
    ['Account', owner ? `${owner}'s account, shared with you` : 'Yours'],
    ['Fallback', fallbackLine(s, p)],
  ]
  if (swarm && Array.isArray(p.execution_choices) && p.execution_choices.length) {
    const workers = (p.execution_choices as Params[]).map((choice) => {
      const worker = sharer(s, str(choice.account_member_id))
      return `${agentLine(s, choice)} on ${worker ? `${worker}'s account` : 'your account'}`
    })
    rows.push(['Workers', workers.join('; ')])
  }
  const brief = str(swarm ? p.objective : p.task)
  if (brief) rows.push([swarm ? 'Objective' : 'Task', brief])
  rows.push(['You hear back', swarm ? hearBack.swarm : hearBack.run])
  return rows.filter(([, value]) => value !== '')
}

export function useLaunchRows(method: string, params: unknown): [string, string][] | undefined {
  const key = useStore((s) => JSON.stringify(launchRows(s, method, params) ?? null))
  return useMemo(() => (JSON.parse(key) as [string, string][] | null) ?? undefined, [key])
}

/**
 * A launch card's title and the assistant's stated reason. The summary
 * quotes the task, which the rows already show in full, so the title stops
 * before it and leaves the rows room on a phone.
 */
export function launchTitle(summary: string): { title: string; reason?: string } {
  const reason = /Assistant's reason: "(.*)"\s*$/s.exec(summary)?.[1]
  const quoted = summary.indexOf(': "')
  return { title: quoted > 0 ? summary.slice(0, quoted) : summary, ...(reason ? { reason } : {}) }
}
