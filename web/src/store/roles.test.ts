import { renderHook } from '@testing-library/react'
import { isAdminRole } from '@/lib/permissions'
import type { Member } from '@/lib/types'
import { useStore } from '@/store'
import { useIsAdmin, useIsSuperAdmin, useIsWorkspaceAdmin, useWorkspaceRole } from '@/store/hooks'
import { alice, serverInfo, workspace } from '@/test/fixtures'

function seed(role: Member['role'], workspaceRole?: Member['role']) {
  useStore.setState({
    info: { ...serverInfo, member: { ...alice, role } },
    workspaces: { [workspace.id]: { ...workspace, role: workspaceRole } },
  })
}

describe('role hooks', () => {
  it('treats super admins and admins as admins, and only super admins as server admins', () => {
    expect(isAdminRole('super_admin')).toBe(true)
    expect(isAdminRole('collaborator')).toBe(false)
    seed('admin')
    expect(renderHook(() => useIsAdmin()).result.current).toBe(true)
    expect(renderHook(() => useIsSuperAdmin()).result.current).toBe(false)
    seed('super_admin')
    expect(renderHook(() => useIsSuperAdmin()).result.current).toBe(true)
  })

  it('takes the workspace role from workspace.list, and the server role when it reports none', () => {
    seed('admin', 'collaborator')
    expect(renderHook(() => useWorkspaceRole(workspace.id)).result.current).toBe('collaborator')
    expect(renderHook(() => useIsWorkspaceAdmin(workspace.id)).result.current).toBe(false)
    seed('admin', 'admin')
    expect(renderHook(() => useIsWorkspaceAdmin(workspace.id)).result.current).toBe(true)
    seed('admin')
    expect(renderHook(() => useIsWorkspaceAdmin(workspace.id)).result.current).toBe(true)
    seed('collaborator')
    expect(renderHook(() => useIsWorkspaceAdmin(workspace.id)).result.current).toBe(false)
  })
})
