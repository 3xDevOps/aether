// The agents surface lists shipped and member-registered harnesses. Adding an
// agent records a member definition after setup in the environment terminal.

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
      <ViewHeader title="Agents" subtitle="the agents this server can launch" />
      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-5xl flex-col gap-5 p-4 sm:p-6">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-3">
              <h2 className="text-base font-semibold">Registered agents</h2>
              {agents && (
                <span className="text-xs text-muted-foreground">
                  {agents.length} {agents.length === 1 ? 'agent' : 'agents'}
                </span>
              )}
            </div>
            <Button size="sm" variant="outline" onClick={refetch}>
              Refresh agents
            </Button>
          </div>
          {error && (
            <p className="rounded-md border border-state-failed/30 bg-state-failed/5 p-3 text-sm text-state-failed">
              {error}
            </p>
          )}
          {loading && (
            <div className="space-y-2 rounded-lg border bg-card p-4">
              <Skeleton className="h-10 w-full rounded-md" />
              <Skeleton className="h-10 w-full rounded-md" />
            </div>
          )}
          {agents && (
            <section aria-label="Registered agents" className="space-y-3">
              <ul className="overflow-hidden rounded-lg border bg-card">
                {agents.map((a) => (
                  <li
                    key={a.name}
                    className="flex flex-wrap items-center gap-3 border-b px-4 py-3 last:border-b-0 sm:px-5"
                  >
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">
                      {a.name}
                    </span>
                    <span
                      className={
                        a.installed === true
                          ? 'rounded-sm bg-state-done/10 px-2 py-1 text-xs text-state-done'
                          : 'rounded-sm bg-muted px-2 py-1 text-xs text-muted-foreground'
                      }
                    >
                      {a.installed === true
                        ? 'Installed'
                        : a.installed === false
                          ? 'Not installed'
                          : 'Installation status unavailable'}
                    </span>
                    <span className="rounded-sm border px-2 py-1 text-xs text-muted-foreground">
                      {a.source === 'shipped' ? 'shipped' : 'member'}
                    </span>
                  </li>
                ))}
                {agents.length === 0 && (
                  <li className="px-4 py-4 text-sm text-muted-foreground sm:px-5">
                    No agents registered yet.
                  </li>
                )}
              </ul>
            </section>
          )}
          {adding ? (
            <AgentWizard
              agents={agents ?? []}
              onRegistered={refetch}
              onCancel={() => setAdding(false)}
            />
          ) : (
            caps.hasMethod('agent.register') && (
              <div className="rounded-lg border bg-card p-4 sm:p-5">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div className="space-y-1">
                    <h2 className="text-base font-semibold">Add an agent</h2>
                    <p className="text-sm text-muted-foreground">
                      Register a member-managed executable and verify its setup.
                    </p>
                  </div>
                  <Button size="sm" onClick={() => setAdding(true)}>
                    Add agent
                  </Button>
                </div>
              </div>
            )
          )}
        </div>
      </main>
    </div>
  )
}

registerRoute('agents', AgentsView)
