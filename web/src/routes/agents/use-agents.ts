import { useCallback, useEffect, useState } from 'react'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentInfo } from '@/lib/types'

export function useAgentList(client: Api = api) {
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

export function agentDisplayNames(agents: AgentInfo[] | null): Record<string, string> {
  return Object.fromEntries((agents ?? []).map((agent) => [agent.name, agent.display_name || agent.name]))
}
