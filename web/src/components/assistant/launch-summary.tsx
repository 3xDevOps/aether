import { useMemo } from 'react'
import type { AssistantConfirmation } from '@/lib/assistant-types'
import { modeLabel } from '@/routes/run/agent-name'
import { useStore, type RootState } from '@/store'

/** The methods whose card says what a launch starts and where. */
const launchMethods = new Set(['run.launch'])

/** Whether `method` starts work, which another member's account may pay for. */
export function isLaunch(method: string): boolean {
  return launchMethods.has(method) || method === 'template.launch'
}

export const hearBack = {
  run: 'Aether notifies you here, on the desktop app and on your phone when it finishes or needs you.',
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

const savedFields: Record<string, string> = {
  harness: 'agent',
  mode: 'mode',
  model: 'model',
  account_member_id: 'account',
  fallback: 'fallback',
  fallback_account_member_ids: 'fallback account',
}

/** The rows a launch card shows, or undefined when `method` launches nothing. */
export function launchRows(s: RootState, method: string, params: unknown, defaults?: AssistantConfirmation['defaults']): [string, string][] | undefined {
  if (!launchMethods.has(method) || params === null || typeof params !== 'object') return undefined
  const p = params as Params
  const workspace = str(p.workspace_id)
  const owner = sharer(s, str(p.account_member_id))
  const extras = Array.isArray(p.extra_workspace_ids) ? p.extra_workspace_ids.filter((id): id is string => typeof id === 'string') : []
  const rows: [string, string][] = [
    ['Kind', 'Session: one agent on one task'],
    ['Workspace', [workspace, ...extras].filter(Boolean).map((id) => s.workspaces[id!]?.name ?? id).join(', ')],
    ['Agent', agentLine(s, p)],
    ['Account', owner ? `${owner}'s account, shared with you` : 'Yours'],
    ['Fallback', fallbackLine(s, p)],
  ]
  if (defaults?.applied?.length) rows.push(['From your saved defaults', defaults.applied.map((field) => savedFields[field] ?? field).join(', ')])
  // Several notes on one field, such as two fallback members that stopped
  // sharing, share a label, so they are one row.
  const noted = (notes: { field: string; reason: string }[] | undefined, verb: string) => {
    const byLabel = new Map<string, string[]>()
    for (const n of notes ?? []) {
      const label = `Saved ${savedFields[n.field] ?? n.field} ${verb}`
      byLabel.set(label, [...(byLabel.get(label) ?? []), n.reason])
    }
    for (const [label, reasons] of byLabel) rows.push([label, reasons.join('; ')])
  }
  noted(defaults?.skipped, 'not used')
  noted(defaults?.unchecked, 'not checked')
  const brief = str(p.task)
  if (brief) rows.push(['Task', brief])
  rows.push(['You hear back', hearBack.run])
  return rows.filter(([, value]) => value !== '')
}

export function useLaunchRows(method: string, params: unknown, defaults?: AssistantConfirmation['defaults']): [string, string][] | undefined {
  const key = useStore((s) => JSON.stringify(launchRows(s, method, params, defaults) ?? null))
  return useMemo(() => (JSON.parse(key) as [string, string][] | null) ?? undefined, [key])
}

/**
 * A launch card's title. The summary quotes the task, which the rows already
 * show in full, so the title stops before it and leaves the rows room on a
 * phone.
 */
export function launchTitle(summary: string): string {
  const quoted = summary.indexOf(': "')
  return quoted > 0 ? summary.slice(0, quoted) : summary
}
