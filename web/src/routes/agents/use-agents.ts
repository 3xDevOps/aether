import { useCallback, useEffect, useState } from 'react'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentInfo } from '@/lib/types'
import { useStore } from '@/store'

/** agent.list inspects the member's home on the server, so one read serves
 * every view until the identity changes; `fresh` re-reads on mount. */
export function useAgentList(client: Api = api, fresh = false) {
  const identity = useStore((s) => s.identityKey)
  const agents = useStore((s) => (s.agentList?.identity === identity ? s.agentList.agents : null))
  const [error, setError] = useState<string | null>(null)
  const reload = useCallback(() => {
    setError(null)
    const at = useStore.getState().identityKey
    client.agentList().then((list) => useStore.getState().setAgentList(at, list), (err: unknown) => setError(message(err)))
  }, [client])
  useEffect(() => {
    const cached = useStore.getState().agentList
    if (fresh || cached?.identity !== identity) reload()
  }, [reload, fresh, identity])
  return { agents, error, reload }
}

export function agentDisplayNames(agents: AgentInfo[] | null): Record<string, string> {
  return Object.fromEntries((agents ?? []).map((agent) => [agent.name, agent.display_name || agent.name]))
}
