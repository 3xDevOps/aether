import type { Workspace } from '@/lib/types'
import type { RootState } from '@/store'
import type { SliceCreator } from '@/store/slice'

export interface WorkspacesSlice {
  workspaces: Record<string, Workspace>
  /** Globally unique IDs: retained across reconnects/identity changes, not reloads. */
  deletedWorkspaceIDs: ReadonlySet<string>
  setWorkspaces: (workspaces: Workspace[]) => void
  upsertWorkspace: (workspace: Workspace) => void
  removeWorkspace: (workspaceID: string) => void
}

function workspaceSnapshot(s: RootState, workspaces: Workspace[]) {
  const byID = Object.fromEntries(
    workspaces.filter((w) => !s.deletedWorkspaceIDs.has(w.id)).map((w) => [w.id, w]),
  )
  const activeWorkspace = byID[s.activeWorkspace]
    ? s.activeWorkspace
    : Object.keys(byID).reduce((first, id) => !first || id < first ? id : first, '')
  const missingWorkspaceRoute =
    s.route.name === 'workspace' && !byID[s.route.params.workspaceId]
  const missingRunRoute =
    s.route.params.runId && s.runs[s.route.params.runId] &&
    !byID[s.runs[s.route.params.runId].workspace_id]
  return {
    workspaces: byID,
    activeWorkspace,
    route: missingWorkspaceRoute
      ? activeWorkspace
        ? { name: 'workspace', params: { workspaceId: activeWorkspace } }
        : { name: 'workspaces', params: {} }
      : missingRunRoute ? { name: 'board', params: {} } : s.route,
  }
}

export const createWorkspacesSlice: SliceCreator<WorkspacesSlice> = (set, get) => ({
  workspaces: {},
  deletedWorkspaceIDs: new Set(),
  setWorkspaces: (workspaces) => set((s) => workspaceSnapshot(s, workspaces)),
  upsertWorkspace: (workspace) =>
    set((s) => s.deletedWorkspaceIDs.has(workspace.id)
      ? s
      : { workspaces: { ...s.workspaces, [workspace.id]: workspace } }),
  removeWorkspace: (workspaceID) => {
    set((s) => {
      const deletedWorkspaceIDs = new Set(s.deletedWorkspaceIDs).add(workspaceID)
      return {
        deletedWorkspaceIDs,
        ...workspaceSnapshot({ ...s, deletedWorkspaceIDs }, Object.values(s.workspaces)),
      }
    })
    const s = get()
    for (const run of Object.values(s.runs)) {
      if (run.workspace_id === workspaceID) s.removeRun(run.id)
    }
  },
})

/**
 * The workspace a form should act on when the member did not pick one.
 * A single workspace needs no picker at all, which is the common case and
 * the same rule the CLI applies.
 */
export function soleWorkspace(workspaces: Record<string, Workspace>): string {
  const ids = Object.keys(workspaces)
  return ids.length === 1 ? ids[0] : ''
}
