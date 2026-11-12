import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { FolderGit2 } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { PageBody } from '@/components/ui/page-body'
import { SectionLabel } from '@/components/ui/section-label'
import { Skeleton } from '@/components/ui/skeleton'
import { ViewHeader } from '@/components/view-header'
import { WorkspaceCreate } from '@/components/workspace-create'
import { api, type Api } from '@/lib/api'
import { useDelayed } from '@/lib/hooks'
import { message } from '@/lib/format'
import type { Workspace } from '@/lib/types'
import { WorkspaceAccessDialog } from '@/routes/workspaces/access-dialog'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { isAdminRole } from '@/lib/permissions'
import { sameIdentity, useStore } from '@/store'
import { useCapability, useIsAdmin, useIsSuperAdmin, useSelfRole, useWorkspaceAccessServer } from '@/store/hooks'

export function WorkspacesRoute({ client = api }: RouteProps & { client?: Api }) {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const selfRole = useSelfRole()
  const superAdmin = useIsSuperAdmin()
  const scoped = useWorkspaceAccessServer()
  const members = useStore((s) => s.members)
  const canAdd = isAdmin && (caps.hasMethod('workspace.add') || caps.hasMethod('workspace.import'))
  const navigate = useStore((s) => s.navigate)
  const active = useStore((s) => s.activeWorkspace)
  const workspaceMap = useStore((s) => s.workspaces)
  const workspaces = useMemo(() => Object.values(workspaceMap), [workspaceMap])
  const [loaded, setLoaded] = useState(useStore.getState().hydrated)
  const [adding, setAdding] = useState(false)
  const [accessing, setAccessing] = useState<Workspace | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [membersError, setMembersError] = useState<string | null>(null)
  const loading = useDelayed(!loaded && error === null)
  const fetchVersion = useRef(0)

  const refetch = useCallback(async () => {
    const version = ++fetchVersion.current
    setError(null)
    try {
      const same = sameIdentity()
      const fresh = await client.workspaceListFull()
      if (version !== fetchVersion.current || !same()) return
      useStore.getState().setWorkspaces(fresh)
      setLoaded(true)
    } catch (err) {
      if (version === fetchVersion.current) setError(message(err))
    }
  }, [client])

  useEffect(() => {
    if (!caps.hasMethod('member.list')) return
    const { readMembers, setMemberList } = useStore.getState()
    const same = sameIdentity()
    setMembersError(null)
    readMembers(client.memberList()).then(
      (list) => same() && setMemberList(list),
      (err: unknown) => same() && setMembersError(`Could not list members: ${message(err)}`),
    )
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
          {membersError && <Callout tone="failed" role="alert">{membersError}</Callout>}
          {error && (
            <Callout tone="failed" role="alert" actions={<Button size="sm" variant="secondary" onClick={() => void refetch()}>Retry</Button>}>
              {error}
            </Callout>
          )}
          <section aria-label="Workspaces" className="flex min-w-0 flex-col gap-2">
            <SectionLabel as="h2">{loaded ? `${workspaces.length} ${workspaces.length === 1 ? 'workspace' : 'workspaces'}` : 'Workspaces'}</SectionLabel>
            {loading && (
              <div role="status" aria-label="Loading workspaces" className="flex flex-col gap-0.5">
                {[0, 1, 2].map((row) => <Skeleton key={row} className="h-11" />)}
              </div>
            )}
            {empty && <p className="text-ui text-muted">{canAdd ? 'No workspaces yet. Add the first one above.' : 'No workspaces yet. An admin adds the first one.'}</p>}
            {loaded && workspaces.length > 0 && (
              <InsetList aria-label="Workspace list">
                {workspaces.map((workspace) => (
                  <InsetListItem key={workspace.id} className="flex-wrap">
                    <FolderGit2 aria-hidden className="size-4 shrink-0 text-muted" />
                    <span className="flex min-w-0 flex-[1_1_12rem] flex-col items-start">
                      <span className="flex max-w-full min-w-0 items-center gap-2">
                        <Button
                          variant="link"
                          size="sm"
                          aria-label={`Open ${workspace.name}`}
                          className="h-auto min-w-0 max-w-full shrink justify-start truncate px-0 text-ui font-medium"
                          onClick={() => open(workspace)}
                        >
                          {workspace.name}
                        </Button>
                        {workspace.id === active && <Badge>Current</Badge>}
                      </span>
                      <span className="max-w-full truncate text-ui-sm text-muted">
                        {[workspace.base_branch, ownerOf(workspace) && `Owner: ${ownerOf(workspace)}`].filter(Boolean).join(' · ')}
                      </span>
                    </span>
                    <span className="flex shrink-0 items-center gap-2 max-sm:pl-7">
                      {scoped && administers(workspace) && caps.hasMethod('workspace.access.list') && (
                        <Button size="sm" variant="secondary" aria-label={`Access to ${workspace.name}`} onClick={() => setAccessing(workspace)}>
                          Access…
                        </Button>
                      )}
                      <Button size="sm" variant="secondary" aria-label={`Settings for ${workspace.name}`} onClick={() => navigate('workspace', { workspaceId: workspace.id })}>
                        Settings
                      </Button>
                    </span>
                  </InsetListItem>
                ))}
              </InsetList>
            )}
          </section>
        </PageBody>
      </div>
      {accessing && (
        <WorkspaceAccessDialog
          workspaceID={accessing.id}
          client={client}
          onClose={() => setAccessing(null)}
          canChangeOwner={superAdmin && caps.hasMethod('workspace.owner.set')}
        />
      )}
    </div>
  )
}

registerRoute('workspaces', WorkspacesRoute)
