import { renderHook } from '@testing-library/react'
import { isAdminRole } from '@/lib/permissions'
import type { Member } from '@/lib/types'
import { useStore } from '@/store'
import { useIsAdmin, useSelf, useIsSuperAdmin, useIsWorkspaceAdmin, useWorkspaceRole } from '@/store/hooks'
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

  it('keeps an old server\'s admin as its server administrator', () => {
    useStore.setState({ info: { ...serverInfo, features: [], member: { ...alice, role: 'admin' } } })
    expect(renderHook(() => useIsSuperAdmin()).result.current).toBe(true)
    useStore.setState({ info: { ...serverInfo, features: [], member: { ...alice, role: 'collaborator' } } })
    expect(renderHook(() => useIsSuperAdmin()).result.current).toBe(false)
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

  it('asks permissions with the role in the run\'s workspace', () => {
    seed('admin', 'collaborator')
    expect(renderHook(() => useSelf(workspace.id)).result.current.role).toBe('collaborator')
    expect(renderHook(() => useSelf()).result.current.role).toBe('admin')
  })
})
