// The agents surface: what harnesses this server can run (shipped and
// member-registered), and `agent add` as a wizard around the agent-setup
// shell. Registration is entirely server-side - a clean shell exit - so the
// list refetches on that signal rather than being written optimistically.

import { useCallback, useEffect, useState } from 'react'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import type { AgentInfo } from '@/lib/types'
import { registerRoute } from '@/routes/registry'
import { AgentWizard } from '@/routes/agents/wizard'
import { useCapability } from '@/store/hooks'

function AgentsView() {
  const caps = useCapability()
  const [agents, setAgents] = useState<AgentInfo[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)

  const refetch = useCallback(() => {
    api
      .agentList()
      .then((list) => {
        setAgents(list)
        setError(null)
      })
      .catch((err) => setError(message(err)))
  }, [])

  useEffect(() => {
    refetch()
  }, [refetch])

  const loading = useDelayed(agents === null && error === null)

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title="Agents" subtitle="harnesses this server can run" />
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-4">
        {error && <p className="text-sm text-state-failed">{error}</p>}
        {loading && (
          <div className="space-y-2">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        )}
        {agents && (
          <ul className="divide-y rounded-md border">
            {agents.map((a) => (
              <li key={a.name} className="flex items-center gap-2 px-3 py-2 text-sm">
                <span className="flex-1 truncate">{a.name}</span>
                <span className="rounded-sm border px-1.5 py-0.5 text-xs text-muted-foreground">
                  {a.source === 'shipped' ? 'shipped' : 'member'}
                </span>
              </li>
            ))}
            {agents.length === 0 && (
              <li className="px-3 py-2 text-sm text-muted-foreground">
                No agents registered yet.
              </li>
            )}
          </ul>
        )}
        {adding ? (
          <AgentWizard
            agents={agents ?? []}
            onRegistered={refetch}
            onCancel={() => setAdding(false)}
          />
        ) : (
          // agent.register lands with the shell's clean exit, and the setup
          // shell itself rides /ws/shell - both must be on this gateway.
          caps.hasMethod('agent.register') &&
          caps.hasWS('shell') && (
            <div>
              <Button size="sm" onClick={() => setAdding(true)}>
                Add agent
              </Button>
            </div>
          )
        )}
      </div>
    </div>
  )
}

registerRoute('agents', AgentsView)
