import type { AgentInfo, LaunchMode } from '@/lib/types'

export const modes: { value: LaunchMode; label: string; description: string }[] = [
  { value: 'tui', label: 'Standard', description: "Your agent's own terminal" },
  { value: 'acp', label: 'Enhanced', description: 'Native messages, approvals and progress' },
  { value: 'headless', label: 'Background', description: 'Runs the task once, no interaction' },
]

export function modeLabel(mode: string): string {
  return modes.find((m) => m.value === mode)?.label ?? mode
}

export function agentLabel(agent: AgentInfo | undefined, name: string): string {
  return agent?.display_name || name
}

export type Refusal = { reason: string; short: string; setUp: boolean }

/** Why `agent` cannot launch in `mode`; `agent` is undefined for the pinned custom harness. */
export function modeRefusal(agent: AgentInfo | undefined, name: string, mode: LaunchMode): Refusal | null {
  if (mode !== 'acp') return null
  const label = agentLabel(agent, name)
  if (!agent?.enhanced || agent.enhanced === 'none') return { reason: `${label} has no Enhanced support.`, short: 'no Enhanced support', setUp: false }
  if (!agent.enhanced_installed) {
    return {
      reason: agent.enhanced === 'adapter' ? `The Enhanced adapter for ${label} is not installed.` : `Enhanced for ${label} is not installed.`,
      short: 'Enhanced not installed',
      setUp: true,
    }
  }
  return null
}

export function refusals(agent: AgentInfo | undefined, name: string): Partial<Record<LaunchMode, Refusal>> {
  return Object.fromEntries(
    modes.flatMap(({ value }) => {
      const refusal = modeRefusal(agent, name, value)
      return refusal ? [[value, refusal]] : []
    }),
  )
}

export function initialMode(agent: AgentInfo | undefined, name: string, remembered: LaunchMode | undefined): LaunchMode {
  const mode = remembered ?? (agent?.default_mode === 'acp' ? 'acp' : 'tui')
  return modeRefusal(agent, name, mode) ? 'tui' : mode
}
