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
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader title="Agents" subtitle="the agents this server can launch" />
      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-[1000px] min-w-0 flex-col gap-4 p-4 sm:p-6">
          <div className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-b bg-sidebar px-3 py-2">
            <div className="min-w-0">
              <h2 className="text-[13px] font-semibold">Registered agents</h2>
              {agents && (
                <p className="text-xs text-muted-foreground">
                  {agents.length} {agents.length === 1 ? 'agent' : 'agents'}
                </p>
              )}
            </div>
            <Button size="sm" variant="outline" onClick={refetch}>
              Refresh agents
            </Button>
          </div>
          {error && (
            <div
              role="alert"
              className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-y border-state-failed/30 bg-state-failed/5 px-3 py-2 text-[13px] text-state-failed"
            >
              <span className="min-w-0 break-words">{error}</span>
              <Button size="sm" variant="outline" onClick={refetch}>
                Retry
              </Button>
            </div>
          )}
          {loading && (
            <div className="space-y-1 border-y py-2">
              <Skeleton className="h-8 w-full rounded-[2px]" />
              <Skeleton className="h-8 w-full rounded-[2px]" />
            </div>
          )}
          {agents && (
            <section aria-label="Registered agents" className="min-w-0">
              <ul className="border-y" aria-label="Agent inventory">
                {agents.map((a) => (
                  <li
                    key={a.name}
                    className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 border-b px-3 py-2.5 last:border-b-0 sm:px-4"
                  >
                    <span className="min-w-0 flex-1 break-all text-[13px] font-medium">
                      {a.name}
                    </span>
                    <span
                      className={
                        a.installed === true
                          ? 'shrink-0 bg-state-done/10 px-1.5 py-0.5 text-xs text-state-done'
                          : 'shrink-0 bg-muted px-1.5 py-0.5 text-xs text-muted-foreground'
                      }
                    >
                      {a.installed === true
                        ? 'Installed'
                        : a.installed === false
                          ? 'Not installed'
                          : 'Installation status unavailable'}
                    </span>
                    <span className="shrink-0 border-l pl-3 text-xs text-muted-foreground">
                      {a.source === 'shipped' ? 'shipped' : 'member'}
                    </span>
                  </li>
                ))}
                {agents.length === 0 && (
                  <li className="px-3 py-4 text-[13px] text-muted-foreground sm:px-4">
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
              <section className="border-y bg-sidebar px-3 py-3 sm:px-4">
                <div className="flex min-w-0 flex-wrap items-center justify-between gap-2">
                  <div className="min-w-0">
                    <h2 className="text-[13px] font-semibold">Add an agent</h2>
                    <p className="text-xs text-muted-foreground">
                      Register a member-managed executable and verify its setup.
                    </p>
                  </div>
                  <Button size="sm" onClick={() => setAdding(true)}>
                    Add agent
                  </Button>
                </div>
              </section>
            )
          )}
        </div>
      </main>
    </div>
  )
}

registerRoute('agents', AgentsView)
