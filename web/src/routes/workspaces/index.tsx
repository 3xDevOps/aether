import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { message, timeAgo } from '@/lib/format'
import { Button } from '@/components/ui/button'
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
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader
        title="Manage workspaces"
        subtitle={workspaces ? `${workspaces.length} total` : 'Workspace scope and defaults'}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-[1200px] min-w-0 flex-col gap-4 p-4 sm:p-6">
          {caps.hasMethod('workspace.add') && (
            <AddForm client={client} onAdded={() => void refetch()} />
          )}

          {loading && (
            <div className="space-y-1 border-y py-2">
              <Skeleton className="h-10 w-full rounded-[2px]" />
              <Skeleton className="h-10 w-full rounded-[2px]" />
            </div>
          )}
          {error && (
            <div
              role="alert"
              className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-y border-state-failed/30 bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed"
            >
              <span className="min-w-0 break-words">{error}</span>
              <Button size="sm" variant="outline" onClick={() => void refetch()}>
                Retry
              </Button>
            </div>
          )}

          <section aria-labelledby="workspace-list-heading" className="min-w-0">
            <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-2 gap-y-1 border-b px-3 py-2">
              <h2 id="workspace-list-heading" className="text-[13px] font-semibold">
                Workspaces
              </h2>
              {workspaces && (
                <span className="text-xs text-muted-foreground">
                  {workspaces.length} {workspaces.length === 1 ? 'workspace' : 'workspaces'}
                </span>
              )}
            </div>

            {workspaces && workspaces.length > 0 ? (
              <ul className="border-b" aria-label="Workspace list">
                {workspaces.map((workspace) => (
                  <li
                    key={workspace.id}
                    className="min-w-0 border-b px-3 py-3 last:border-b-0 sm:px-4"
                  >
                    <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
                      <div className="min-w-0 flex-1">
                        <h3 className="break-all text-[13px] font-semibold" title={workspace.name}>
                          {workspace.name}
                        </h3>
                        <p className="mt-1 text-xs text-muted-foreground">
                          Created <time dateTime={workspace.created_at}>{timeAgo(workspace.created_at)}</time>
                        </p>
                      </div>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => navigate('workspace', { workspaceId: workspace.id })}
                      >
                        Open
                      </Button>
                    </div>
                    <dl className="mt-3 grid min-w-0 gap-2 border-t pt-3 text-xs sm:grid-cols-2">
                      <div className="min-w-0">
                        <dt className="text-muted-foreground">Base branch</dt>
                        <dd className="mt-1 break-all font-mono text-foreground" title={workspace.base_branch}>
                          {workspace.base_branch}
                        </dd>
                      </div>
                      <div className="min-w-0">
                        <dt className="text-muted-foreground">Steering policy</dt>
                        <dd className="mt-1 break-words text-foreground">
                          {workspace.steer_others === 'admins_only'
                            ? 'Admins steer other members'
                            : 'Everyone with steer'}
                        </dd>
                      </div>
                    </dl>
                  </li>
                ))}
              </ul>
            ) : workspaces ? (
              <div className="border-b border-dashed px-3 py-8 text-center">
                <p className="text-[13px] font-medium">No workspaces yet.</p>
                {caps.hasMethod('workspace.add') && (
                  <p className="mt-1 text-xs text-muted-foreground">
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
      className="w-full border-y bg-sidebar px-3 py-3 sm:px-4"
      aria-label="Add workspace"
      onSubmit={(e) => {
        e.preventDefault()
        void add()
      }}
    >
      <div className="mb-3">
        <h2 className="text-[13px] font-semibold">Add workspace</h2>
        <p className="mt-1 text-xs leading-5 text-muted-foreground">
          Set a name and the base branch new runs should start from.
        </p>
      </div>
      <div className="grid min-w-0 gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-end">
        <Label className="block min-w-0 space-y-1">
          Name
          <Input
            value={name}
            placeholder="team"
            onChange={(e) => setName(e.target.value)}
          />
        </Label>
        <Label className="block min-w-0 space-y-1">
          Base branch
          <Input
            value={baseBranch}
            onChange={(e) => setBaseBranch(e.target.value)}
          />
        </Label>
        <Button
          className="w-full sm:w-auto"
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
