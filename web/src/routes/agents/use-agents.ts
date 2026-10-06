import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import type { AgentInfo } from '@/lib/types'
import { useCapability } from '@/store/hooks'

export type AgentsState = AgentInfo[] | 'loading' | 'unknown'

export function useAgents(): AgentsState {
  const caps = useCapability()
  const listable = caps.hasMethod('agent.list')
  const [agents, setAgents] = useState<AgentsState>('loading')
  useEffect(() => {
    if (!listable) return
    let live = true
    api
      .agentList()
      .then((list) => live && setAgents(list))
      .catch(() => live && setAgents('unknown'))
    return () => {
      live = false
    }
  }, [listable])
  return listable ? agents : 'unknown'
}

export function agentDisplayNames(agents: AgentsState): Record<string, string> {
  return Array.isArray(agents) ? Object.fromEntries(agents.map((agent) => [agent.name, agent.display_name || agent.name])) : {}
}
