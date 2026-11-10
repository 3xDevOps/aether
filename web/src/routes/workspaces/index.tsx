import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Ellipsis, FolderGit2 } from '@/components/icons'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { ListRow } from '@/components/ui/list-row'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { PageBody } from '@/components/ui/page-body'
import { SectionLabel } from '@/components/ui/section-label'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { WorkspaceCreate } from '@/components/workspace-create'
import { api, type Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import { message } from '@/lib/format'
import type { Workspace } from '@/lib/types'
import { WorkspaceSettingsDialog } from '@/routes/admin-dialogs'
import { WorkspaceAccessDialog, WorkspaceOwnerDialog } from '@/routes/workspaces/access-dialog'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { isAdminRole } from '@/lib/permissions'
import { useStore } from '@/store'
import { useCapability, useIsAdmin, useIsSuperAdmin, useSelfRole } from '@/store/hooks'

export function WorkspacesRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const selfRole = useSelfRole()
  const superAdmin = useIsSuperAdmin()
  const members = useStore((s) => s.members)
  const canAdd = isAdmin && (caps.hasMethod('workspace.add') || caps.hasMethod('workspace.import'))
  const navigate = useStore((s) => s.navigate)
  const active = useStore((s) => s.activeWorkspace)
  const workspaceMap = useStore((s) => s.workspaces)
  const workspaces = useMemo(() => Object.values(workspaceMap), [workspaceMap])
  const [loaded, setLoaded] = useState(useStore.getState().hydrated)
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<Workspace | null>(null)
  const [settings, setSettings] = useState<Workspace | null>(null)
  const [accessing, setAccessing] = useState<Workspace | null>(null)
  const [chowning, setChowning] = useState<Workspace | null>(null)
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
    if (!caps.hasMethod('member.list')) return
    const { readMembers, setMemberList } = useStore.getState()
    readMembers(client.memberList()).then(setMemberList, () => {})
  }, [caps, client])

  useEffect(() => {
    void refetch()
    return () => { fetchVersion.current += 1 }
  }, [refetch])

  const administers = (workspace: Workspace) => isAdminRole(workspace.role ?? selfRole)
  const selfID = useStore((s) => s.info?.member.id)
  const ownerOf = (workspace: Workspace) => {
    const id = workspace.owner_member_id
    if (!id) return ''
    return id === selfID ? 'you' : members[id]?.display_name ?? id
  }

  const open = (workspace: Workspace) => {
    useStore.getState().setActiveWorkspace(workspace.id)
    navigate('board')
  }

  const empty = loaded && workspaces.length === 0
  // Sticky, so the form outlives the first workspace it creates: an import
  // still shows its outcome and deploy key after the list stops being empty.
  if (empty && canAdd && !adding) setAdding(true)
  const showCreate = canAdd && adding

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
        <PageBody>
          <p className="text-ui text-muted">
            A workspace is one repository and the base branch its sessions start from. The sidebar shows one workspace at a time.
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
            {loading && (
              <div role="status" aria-label="Loading workspaces" className="flex flex-col gap-0.5">
                {[0, 1, 2].map((row) => <Skeleton key={row} className="h-7" />)}
              </div>
            )}
            {empty && <p className="text-ui text-muted">{canAdd ? 'No workspaces yet. Add the first one above.' : 'No workspaces yet. An admin adds the first one.'}</p>}
            {loaded && workspaces.length > 0 && (
              <ul aria-label="Workspace list" className="flex flex-col gap-0.5">
                {workspaces.map((workspace) => (
                  <li key={workspace.id}>
                    <ListRow
                      aria-label={`Open ${workspace.name}`}
                      leading={<FolderGit2 />}
                      trailing={[workspace.id === active ? `Current · ${workspace.base_branch}` : workspace.base_branch, ownerOf(workspace) && `Owner: ${ownerOf(workspace)}`].filter(Boolean).join(' · ')}
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
                            {caps.hasMethod('workspace.environment.get') && (
                              <MenuItem onSelect={() => navigate('workspace', { workspaceId: workspace.id, section: 'environment' })}>Environment</MenuItem>
                            )}
                            {administers(workspace) && caps.hasMethod('workspace.settings') && <MenuItem onSelect={() => setSettings(workspace)}>Settings…</MenuItem>}
                            {administers(workspace) && caps.hasMethod('workspace.access.list') && <MenuItem onSelect={() => setAccessing(workspace)}>Access…</MenuItem>}
                            {superAdmin && caps.hasMethod('workspace.owner.set') && <MenuItem onSelect={() => setChowning(workspace)}>Change owner…</MenuItem>}
                            {administers(workspace) && caps.hasMethod('workspace.delete') && (
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
        </PageBody>
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
      {accessing && <WorkspaceAccessDialog workspaceID={accessing.id} client={client} onClose={() => setAccessing(null)} />}
      {chowning && <WorkspaceOwnerDialog workspaceID={chowning.id} client={client} onClose={() => setChowning(null)} />}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          description="This permanently deletes the workspace, its finished sessions, retained containers, files, history and server repository. Members, their environments and external repositories stay."
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
            The server refuses while a session is unfinished, review work or cleanup is pending, or a schedule remains.
          </p>
        </Confirm>
      )}
    </div>
  )
}

registerRoute('workspaces', WorkspacesRoute)
