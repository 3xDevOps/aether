import { useId } from 'react'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { isAdminRole } from '@/lib/permissions'
import type { Member, Workspace } from '@/lib/types'

/** Workspaces the caller administers. A server that reports no role leaves the server role to decide. */
export function administered(workspaces: Record<string, Workspace>, selfRole: Member['role'] | null): Workspace[] {
  return Object.values(workspaces).filter((w) => isAdminRole(w.role ?? selfRole))
}

/** Roles the caller may hand to an invitee: only a super admin invites an admin. */
export function invitableRoles(selfRole: Member['role'] | null): Member['role'][] {
  return selfRole === 'super_admin' ? ['viewer', 'collaborator', 'admin', 'super_admin'] : ['viewer', 'collaborator']
}

/** Whether an invitation may be sent: anyone but a super admin names at least one workspace. */
export function workspacesValid(selfRole: Member['role'] | null, picked: string[]): boolean {
  return selfRole === 'super_admin' || picked.length > 0
}

export function WorkspacePicker({ workspaces, picked, required, onChange }: {
  workspaces: Workspace[]
  picked: string[]
  required: boolean
  onChange: (picked: string[]) => void
}) {
  const id = useId()
  return (
    <fieldset className="flex min-w-0 flex-col gap-1.5">
      <legend className="mb-1 text-ui-sm font-medium text-text">Workspaces</legend>
      <p className="text-ui-sm text-muted">
        {required ? 'The member reaches only the workspaces you pick. Pick at least one.' : 'Leave all unchecked to grant workspaces later from Manage workspaces.'}
      </p>
      {workspaces.length === 0 && <p className="text-ui-sm text-muted">You can invite once you own a workspace. Ask a super admin.</p>}
      <div className="flex max-h-40 min-w-0 flex-col gap-1.5 overflow-y-auto">
        {workspaces.map((workspace) => (
          <div key={workspace.id} className="flex min-w-0 items-center gap-2">
            <Checkbox
              id={`${id}-${workspace.id}`}
              checked={picked.includes(workspace.id)}
              onCheckedChange={(checked) => onChange(checked === true ? [...picked, workspace.id] : picked.filter((p) => p !== workspace.id))}
            />
            <Label htmlFor={`${id}-${workspace.id}`} className="min-w-0 truncate font-normal">{workspace.name}</Label>
          </div>
        ))}
      </div>
    </fieldset>
  )
}
