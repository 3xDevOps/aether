import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { message, timeAgo } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Chip } from '@/components/ui/heroui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import type { Workspace } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { useDelayed } from '@/lib/hooks'

export function WorkspacesRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  const navigate = useStore((s) => s.navigate)
  const [workspaces, setWorkspaces] = useState<Workspace[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loading = useDelayed(workspaces === null && error === null)

  const refetch = useCallback(async () => {
    setError(null)
    try {
      setWorkspaces(await client.workspaceListFull())
    } catch (err) {
      setError(message(err))
    }
  }, [client])

  useEffect(() => {
    void refetch()
  }, [refetch])

  return (
    <div className="flex h-full min-w-0 flex-col">
      <ViewHeader
        title="Manage workspaces"
        subtitle={workspaces ? `${workspaces.length} total` : 'Workspace scope and defaults'}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-[1200px] flex-col gap-6 p-4 sm:p-6">
          {caps.hasMethod('workspace.add') && (
            <AddForm client={client} onAdded={() => void refetch()} />
          )}

          {loading && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Skeleton className="h-36 rounded-lg" />
              <Skeleton className="h-36 rounded-lg" />
            </div>
          )}
          {error && (
            <div
              role="alert"
              className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-state-failed/30 bg-state-failed/10 p-4 text-sm text-state-failed"
            >
              <span>{error}</span>
              <Button size="sm" variant="outline" onClick={() => void refetch()}>
                Retry
              </Button>
            </div>
          )}

          <section aria-labelledby="workspace-list-heading">
            <div className="mb-3 flex flex-wrap items-end justify-between gap-2">
              <div>
                <h2 id="workspace-list-heading" className="text-base font-semibold">
                  Workspaces
                </h2>
                <p className="mt-1 text-[13px] text-muted-foreground">
                  Choose a workspace to change the active scope.
                </p>
              </div>
              {workspaces && (
                <Chip color="default" variant="soft" size="sm">
                  <Chip.Label>{workspaces.length} total</Chip.Label>
                </Chip>
              )}
            </div>

            {workspaces && workspaces.length > 0 ? (
              <ul className="grid gap-3 lg:grid-cols-2">
                {workspaces.map((workspace) => (
                  <li
                    key={workspace.id}
                    className="rounded-lg border bg-card p-4 shadow-xs transition-colors hover:border-foreground/20 sm:p-5"
                  >
                    <div className="flex items-start gap-3">
                      <div className="min-w-0 flex-1">
                        <h3 className="truncate text-base font-semibold tracking-tight" title={workspace.name}>
                          {workspace.name}
                        </h3>
                        <p className="mt-1 text-[13px] text-muted-foreground">
                          Created <time dateTime={workspace.created_at}>{timeAgo(workspace.created_at)}</time>
                        </p>
                      </div>
                      {/* Opening a workspace is also how a member changes scope:
                          every other surface follows activeWorkspace. */}
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => navigate('workspace', { workspaceId: workspace.id })}
                      >
                        Open
                      </Button>
                    </div>
                    <dl className="mt-4 grid gap-3 border-t pt-4 text-[13px] sm:grid-cols-2">
                      <div className="min-w-0">
                        <dt className="text-muted-foreground">Base branch</dt>
                        <dd className="mt-1 truncate font-mono text-foreground" title={workspace.base_branch}>
                          {workspace.base_branch}
                        </dd>
                      </div>
                      <div className="min-w-0">
                        <dt className="text-muted-foreground">Steering policy</dt>
                        <dd className="mt-1">
                          <Chip color="default" variant="tertiary" size="sm">
                            <Chip.Label>
                              {workspace.steer_others === 'admins_only'
                                ? 'admins steer others'
                                : 'everyone with steer'}
                            </Chip.Label>
                          </Chip>
                        </dd>
                      </div>
                    </dl>
                  </li>
                ))}
              </ul>
            ) : workspaces ? (
              <div className="rounded-lg border border-dashed p-8 text-center">
                <p className="text-sm font-medium">No workspaces yet.</p>
                {caps.hasMethod('workspace.add') && (
                  <p className="mt-1 text-[13px] text-muted-foreground">
                    Add one above to give runs a shared scope.
                  </p>
                )}
              </div>
            ) : null}
          </section>
        </div>
      </div>
    </div>
  )
}

function AddForm({ client, onAdded }: { client: Api; onAdded: () => void }) {
  const [name, setName] = useState('')
  const [baseBranch, setBaseBranch] = useState('main')
  const [busy, setBusy] = useState(false)

  const add = async () => {
    setBusy(true)
    try {
      await client.workspaceAdd({
        name: name.trim(),
        base_branch: baseBranch.trim(),
        environment: {},
      })
      setName('')
      onAdded()
      toast.success('Workspace added')
    } catch (err) {
      toast.error(message(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      className="w-full max-w-3xl rounded-lg border bg-card p-4 shadow-xs sm:p-5"
      aria-label="Add workspace"
      onSubmit={(e) => {
        e.preventDefault()
        void add()
      }}
    >
      <div className="mb-4">
        <h2 className="text-base font-semibold">Add workspace</h2>
        <p className="mt-1 text-[13px] leading-5 text-muted-foreground">
          Set a name and the base branch new runs should start from.
        </p>
      </div>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-end">
        <Label className="space-y-1.5">
          Name
          <Input
            value={name}
            placeholder="team"
            onChange={(e) => setName(e.target.value)}
          />
        </Label>
        <Label className="space-y-1.5">
          Base branch
          <Input
            value={baseBranch}
            onChange={(e) => setBaseBranch(e.target.value)}
          />
        </Label>
        <Button
          type="submit"
          disabled={busy || !name.trim() || !baseBranch.trim()}
        >
          Add
        </Button>
      </div>
    </form>
  )
}

registerRoute('workspaces', WorkspacesRoute)
