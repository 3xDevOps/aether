import type { Workspace } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface WorkspacesSlice {
  workspaces: Record<string, Workspace>
  setWorkspaces: (workspaces: Workspace[]) => void
  upsertWorkspace: (workspace: Workspace) => void
}

export const createWorkspacesSlice: SliceCreator<WorkspacesSlice> = (set) => ({
  workspaces: {},
  setWorkspaces: (workspaces) =>
    set((s) => {
      const byID = Object.fromEntries(workspaces.map((w) => [w.id, w]))
      const activeWorkspace = byID[s.activeWorkspace]
        ? s.activeWorkspace
        : workspaces.reduce((first, w) => !first || w.id < first ? w.id : first, '')
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
    }),
  upsertWorkspace: (workspace) =>
    set((s) => ({ workspaces: { ...s.workspaces, [workspace.id]: workspace } })),
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
