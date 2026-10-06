import { agentLabel } from '@/components/launch/modes'
import type { AgentInfo, LaunchMode } from '@/lib/types'

const adapters: Record<string, string> = {
  claude: 'an adapter published by Anthropic, Zed and JetBrains',
  codex: 'an adapter from the ACP registry',
  pi: 'an experimental community adapter',
}

const logins: Record<string, { command: string; hint: string }> = {
  claude: { command: 'claude', hint: 'Start Claude Code, then type /login.' },
  codex: { command: 'codex login', hint: 'Choose the device code option.' },
  pi: { command: 'pi', hint: 'Start pi, then type /login.' },
  omp: { command: 'omp', hint: 'Start oh-my-pi and follow its login.' },
  opencode: { command: 'opencode auth login', hint: 'Pick your provider.' },
}

export function label(agent: AgentInfo): string {
  return agentLabel(agent, agent.name)
}

export function enhancedSupported(agent: AgentInfo): boolean {
  return agent.enhanced === 'native' || agent.enhanced === 'adapter'
}

export function enhancedUnavailable(agent: AgentInfo): string | null {
  if (enhancedSupported(agent)) return null
  return agent.source === 'member'
    ? `${label(agent)}: not available. Add an enhanced command to the agent to use it.`
    : `${label(agent)}: not available. The agent does not speak the Agent Client Protocol.`
}

export function defaultMode(agent: AgentInfo, remembered: LaunchMode | undefined): LaunchMode {
  if (remembered === 'acp' && !enhancedSupported(agent)) return 'tui'
  return remembered ?? (agent.default_mode === 'acp' ? 'acp' : 'tui')
}

export function setupMode(agent: AgentInfo, remembered: LaunchMode | undefined): 'tui' | 'acp' {
  if (!remembered && agent.enhanced_default && enhancedSupported(agent)) return 'acp'
  return defaultMode(agent, remembered) === 'acp' ? 'acp' : 'tui'
}

export function supportWords(agent: AgentInfo): string {
  return enhancedSupported(agent) ? 'Standard · Enhanced' : 'Standard'
}

export function loginFor(agent: AgentInfo): { command: string; hint: string } | null {
  return agent.source === 'shipped' ? logins[agent.name] ?? null : null
}

export function ready(agent: AgentInfo): boolean {
  return agent.installed === true && agent.login_found !== false
}

export function modeLines(agent: AgentInfo): { term: string; text: string }[] {
  if (!enhancedSupported(agent)) return []
  const name = label(agent)
  const lines = [
    {
      term: 'Support',
      text: agent.enhanced === 'native'
        ? `${name}: supported by the agent's own CLI`
        : `${name}: supported through ${adapters[agent.name] ?? 'an adapter'}`,
    },
    {
      term: 'Setup',
      text: agent.enhanced_installed
        ? 'Already installed'
        : agent.enhanced === 'native'
          ? 'Comes with the agent'
          : 'Installed with the agent in the next step',
    },
    { term: 'Switching', text: agent.switchable ? 'Switch a running agent from its header' : 'Chosen when the run starts' },
    {
      term: 'Fallback',
      text: `If ${agent.enhanced === 'native' ? 'Enhanced' : 'the adapter'} fails to start, the run tells you why and offers the terminal`,
    },
  ]
  if (agent.name === 'claude') {
    lines.push({ term: 'Billing', text: 'Uses your Claude login through the Claude Agent SDK; the run shows which account pays' })
  }
  return lines
}
