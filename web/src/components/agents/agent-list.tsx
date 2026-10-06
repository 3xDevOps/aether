import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { enhancedSupported, label, ready, supportWords } from '@/components/agents/agent-copy'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { StatusDot } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentInfo } from '@/lib/types'

export function useAgentList(client: Api) {
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const reload = useCallback(() => {
    setError(null)
    client.agentList().then(setAgents, (err: unknown) => setError(message(err)))
  }, [client])
  useEffect(() => {
    reload()
  }, [reload])
  return { agents, error, reload }
}

function facts(agent: AgentInfo): string {
  if (agent.installed !== true) return 'Not installed'
  if (agent.login_found === undefined) return 'Installed'
  return agent.login_found ? 'Installed · Login found' : 'Installed · No login found'
}

export function AgentList({
  agents,
  onSetUp,
  onRun,
  extra,
}: {
  agents: AgentInfo[]
  /** Absent when this gateway cannot set agents up. */
  onSetUp?: (agent: AgentInfo) => void
  onRun?: (agent: AgentInfo) => void
  extra?: (agent: AgentInfo) => ReactNode
}) {
  return (
    <ul aria-label="Agents" className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
      {agents.map((agent) => {
        const name = label(agent)
        const runnable = ready(agent) && onRun
        return (
          <li key={agent.name} className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5">
            <AgentGlyph agent={agent.glyph ?? agent.name} colored className="size-5" />
            <span className="flex min-w-[60%] flex-1 flex-col">
              <span className="truncate text-ui font-medium text-text">{name}</span>
              <span className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-ui-sm text-muted">
                <StatusDot tone={ready(agent) ? 'done' : 'neutral'} />
                <span>{facts(agent)}</span>
                <span aria-hidden>·</span>
                <span aria-label={enhancedSupported(agent) ? 'Supports Standard and Enhanced' : 'Supports Standard only'}>{supportWords(agent)}</span>
              </span>
            </span>
            <span className="ml-auto flex items-center gap-2">
            {extra?.(agent)}
            {runnable && onSetUp && (
              <Button size="sm" variant="ghost" aria-label={`Set up ${name} again`} onClick={() => onSetUp(agent)}>
                Set up
              </Button>
            )}
            {runnable ? (
              <Button size="sm" variant="secondary" aria-label={`Run ${name}`} onClick={() => onRun(agent)}>
                Run
              </Button>
            ) : (
              onSetUp && (
                <Button size="sm" variant="secondary" aria-label={`Set up ${name}`} onClick={() => onSetUp(agent)}>
                  Set up
                </Button>
              )
            )}
            </span>
          </li>
        )
      })}
    </ul>
  )
}
