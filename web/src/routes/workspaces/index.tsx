import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Ellipsis, FolderGit2 } from '@/components/icons'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { ListRow } from '@/components/ui/list-row'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { SectionLabel } from '@/components/ui/section-label'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { WorkspaceCreate } from '@/components/workspace-create'
import { api, type Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import { message } from '@/lib/format'
import type { Workspace } from '@/lib/types'
import { WorkspaceSettingsDialog } from '@/routes/admin-dialogs'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'

export function WorkspacesRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const canDelete = isAdmin && caps.hasMethod('workspace.delete')
  const canSettings = isAdmin && caps.hasMethod('workspace.settings')
  const canAdd = isAdmin && (caps.hasMethod('workspace.add') || caps.hasMethod('workspace.import'))
  const navigate = useStore((s) => s.navigate)
  const active = useStore((s) => s.activeWorkspace)
  const workspaceMap = useStore((s) => s.workspaces)
  const workspaces = useMemo(() => Object.values(workspaceMap), [workspaceMap])
  const [loaded, setLoaded] = useState(useStore.getState().hydrated)
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<Workspace | null>(null)
  const [settings, setSettings] = useState<Workspace | null>(null)
  const [error, setError] = useState<string | null>(null)
  const loading = useDelayed(!loaded && error === null)
  const fetchVersion = useRef(0)

  const refetch = useCallback(async () => {
    const version = ++fetchVersion.current
    setError(null)
    try {
      const fresh = await client.workspaceListFull()
      if (version !== fetchVersion.current) return
      useStore.getState().setWorkspaces(fresh)
      setLoaded(true)
    } catch (err) {
      if (version === fetchVersion.current) setError(message(err))
    }
  }, [client])

  useEffect(() => {
    void refetch()
    return () => { fetchVersion.current += 1 }
  }, [refetch])

  const open = (workspace: Workspace) => {
    useStore.getState().setActiveWorkspace(workspace.id)
    navigate('board')
  }

  const empty = loaded && workspaces.length === 0
  const showCreate = canAdd && (adding || empty)

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <ViewHeader
        title="Manage workspaces"
        actions={canAdd && !empty && (
          <Button size="sm" variant={adding ? 'secondary' : 'primary'} aria-expanded={adding} onClick={() => setAdding((v) => !v)}>
            {adding ? 'Cancel' : 'Add workspace'}
          </Button>
        )}
      />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-8 px-4 py-6 sm:px-6">
          <p className="text-ui text-muted">
            A workspace is one repository and the base branch its runs start from. The sidebar shows one workspace at a time.
          </p>
          {showCreate && (
            <div className="rounded-panel border border-seam p-4">
              <WorkspaceCreate client={client} onRefresh={() => void refetch()} onCreated={(workspace, source) => {
                useStore.getState().upsertWorkspace(workspace)
                setAdding(false)
                navigate('workspace', { workspaceId: workspace.id, repository: source })
              }} />
            </div>
          )}
          {error && (
            <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={() => void refetch()}>Retry</Button>}>
              {error}
            </Callout>
          )}
          <section aria-label="Workspaces" className="flex min-w-0 flex-col gap-2">
            <SectionLabel as="h2">{loaded ? `${workspaces.length} ${workspaces.length === 1 ? 'workspace' : 'workspaces'}` : 'Workspaces'}</SectionLabel>
            {loading && <div className="h-16"><Skeleton className="size-full" /></div>}
            {empty && <p className="text-ui text-muted">{canAdd ? 'No workspaces yet. Add the first one above.' : 'No workspaces yet. An admin adds the first one.'}</p>}
            {loaded && workspaces.length > 0 && (
              <ul aria-label="Workspace list" className="flex flex-col gap-0.5">
                {workspaces.map((workspace) => (
                  <li key={workspace.id}>
                    <ListRow
                      aria-label={`Open ${workspace.name}`}
                      leading={<FolderGit2 />}
                      trailing={workspace.id === active ? `Current · ${workspace.base_branch}` : workspace.base_branch}
                      onClick={() => open(workspace)}
                      hoverAction={(
                        <Menu>
                          <MenuTrigger asChild>
                            <Button variant="ghost" size="icon-sm" label={`More actions for ${workspace.name}`}>
                              <Ellipsis />
                            </Button>
                          </MenuTrigger>
                          <MenuContent align="end">
                            <MenuItem onSelect={() => navigate('workspace', { workspaceId: workspace.id })}>Repository</MenuItem>
                            {canSettings && <MenuItem onSelect={() => setSettings(workspace)}>Settings…</MenuItem>}
                            {canDelete && (
                              <>
                                <MenuSeparator />
                                <MenuItem onSelect={() => setDeleting(workspace)}>Delete…</MenuItem>
                              </>
                            )}
                          </MenuContent>
                        </Menu>
                      )}
                    >
                      {workspace.name}
                    </ListRow>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      </div>
      {settings && (
        <WorkspaceSettingsDialog
          workspaceID={settings.id}
          client={client}
          onRepository={() => {
            setSettings(null)
            navigate('workspace', { workspaceId: settings.id })
          }}
          onClose={() => setSettings(null)}
        />
      )}
      {deleting && canDelete && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          description="This permanently deletes the workspace, its finished runs, retained containers, files, history and server repository. Members, their environments and external repositories stay."
          action="Delete workspace"
          onConfirm={() => client.workspaceDelete(deleting.id)}
          onDone={() => {
            toast.success(`${deleting.name} deleted`)
            useStore.getState().removeWorkspace(deleting.id)
            void refetch()
          }}
          onClose={() => setDeleting(null)}
        >
          <p className="text-ui-sm text-muted">
            The server refuses while a run is unfinished, swarm or review work or cleanup is pending, or a schedule remains.
          </p>
        </Confirm>
      )}
    </div>
  )
}

registerRoute('workspaces', WorkspacesRoute)
