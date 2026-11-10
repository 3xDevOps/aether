import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import { isAdminRole } from '@/lib/permissions'
import type { Member, WorkspaceAccess } from '@/lib/types'
import { roleLabel } from '@/routes/members/roster'
import { sameIdentity, useStore } from '@/store'
import { useIsSuperAdmin } from '@/store/hooks'

async function refreshWorkspaces(client: Api) {
  const same = sameIdentity()
  const list = await client.workspaceListFull()
  if (same()) useStore.getState().setWorkspaces(list)
}

function useMembers(client: Api): Member[] {
  const members = useStore((s) => s.members)
  useEffect(() => {
    let live = true
    const same = sameIdentity()
    useStore.getState().readMembers(client.memberList()).then((list) => live && same() && useStore.getState().setMemberList(list), () => {})
    return () => {
      live = false
    }
  }, [client])
  return Object.values(members).filter((m) => !m.pending)
}

function useSelfID(): string | undefined {
  return useStore((s) => s.info?.member.id)
}

function PickerName({ member, selfID }: { member: Member; selfID: string | undefined }) {
  return <>{member.display_name}{member.id === selfID ? ' (you)' : ''}</>
}

function ownerChoice(member: Member, selfID: string | undefined, current: string) {
  const tags = [member.id === selfID && 'you', member.id === current && 'current owner'].filter(Boolean)
  return tags.length ? `${member.display_name} (${tags.join(', ')})` : member.display_name
}

function grantedRole(member: Member) {
  return member.role === 'admin' ? 'Admin · collaborator here' : roleLabel[member.role]
}

function MemberLine({ member, id, detail, selfID }: { member?: Member; id: string; detail: string; selfID: string | undefined }) {
  return (
    <>
      <Avatar name={member?.display_name ?? id} color={member?.color} size="header" />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-ui font-medium text-text">{member ? <PickerName member={member} selfID={selfID} /> : id}</span>
        <span className="truncate text-ui-sm text-muted">{detail}</span>
      </span>
    </>
  )
}

export function WorkspaceAccessDialog({ workspaceID, client, onClose }: { workspaceID: string; client: Api; onClose: () => void }) {
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const superAdmin = useIsSuperAdmin()
  const members = useMembers(client)
  const byID = new Map(members.map((m) => [m.id, m]))
  const [access, setAccess] = useState<WorkspaceAccess | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [pick, setPick] = useState('')
  const selfID = useSelfID()
  const returnFocus = useReturnFocus()

  const load = useCallback(async () => {
    try {
      setAccess(await client.workspaceAccessList(workspaceID))
    } catch (err) {
      setError(message(err))
    }
  }, [client, workspaceID])

  useEffect(() => {
    void load()
  }, [load])

  const change = async (send: () => Promise<unknown>, done: string) => {
    setBusy(true)
    setError(null)
    try {
      await send()
      toast.success(done)
      await Promise.all([load(), refreshWorkspaces(client)])
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const ownerMember = access ? byID.get(access.owner_member_id) : undefined
  const granted = new Set([access?.owner_member_id, ...(access?.grants.map((g) => g.member_id) ?? [])])
  const addable = members.filter((m) => !granted.has(m.id) && m.role !== 'super_admin' && (superAdmin || !isAdminRole(m.role)))

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-lg" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Access to {workspace?.name ?? 'this workspace'}</DialogTitle>
          <DialogDescription>
            The owner and the members listed here can use this workspace. Granted admins work here as collaborators. Super admins can use every workspace.
          </DialogDescription>
        </DialogHeader>
        <div className="flex min-w-0 flex-col gap-3">
          {error && <Callout tone="failed" role="alert">{error}</Callout>}
          {!access && !error && (
            <div role="status" aria-label="Loading access" className="flex flex-col gap-0.5">
              {[0, 1].map((row) => <Skeleton key={row} className="h-7" />)}
            </div>
          )}
          {access && (
            <InsetList aria-label="Members with access">
              <InsetListItem>
                <MemberLine member={byID.get(access.owner_member_id)} id={access.owner_member_id} detail={ownerMember ? `Owner · ${roleLabel[ownerMember.role]}` : 'Owner'} selfID={selfID} />
              </InsetListItem>
              {access.grants.map((grant) => {
                const member = byID.get(grant.member_id)
                const name = member?.display_name ?? grant.member_id
                return (
                  <InsetListItem key={grant.member_id}>
                    <MemberLine member={member} id={grant.member_id} detail={member ? grantedRole(member) : 'Granted'} selfID={selfID} />
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={busy}
                      aria-label={`Remove ${name}`}
                      onClick={() => void change(() => client.workspaceAccessRevoke(workspaceID, grant.member_id), `${name} no longer reaches ${workspace?.name ?? 'the workspace'}`)}
                    >
                      Remove
                    </Button>
                  </InsetListItem>
                )
              })}
            </InsetList>
          )}
          {access?.grants.length === 0 && <p className="text-ui-sm text-muted">No one else has access yet.</p>}
          {addable.length === 0 ? (
            <p className="text-ui-sm text-muted">Everyone already has access.</p>
          ) : (
          <div className="flex min-w-0 items-end gap-2">
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <Label htmlFor="access-member">Add a member</Label>
              <Select value={pick} onValueChange={setPick}>
                <SelectTrigger id="access-member"><SelectValue placeholder="Choose a member" /></SelectTrigger>
                <SelectContent>
                  {addable.map((m) => <SelectItem key={m.id} value={m.id} detail={grantedRole(m)}><PickerName member={m} selfID={selfID} /></SelectItem>)}
                </SelectContent>
              </Select>
            </div>
            <Button
              disabled={busy || pick === ''}
              onClick={() => {
                const name = byID.get(pick)?.display_name ?? pick
                void change(() => client.workspaceAccessGrant(workspaceID, [pick]), `${name} now reaches ${workspace?.name ?? 'the workspace'}`).then(() => setPick(''))
              }}
            >
              Add
            </Button>
          </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function WorkspaceOwnerDialog({ workspaceID, client, onClose }: { workspaceID: string; client: Api; onClose: () => void }) {
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const members = useMembers(client).filter((m) => isAdminRole(m.role))
  const current = workspace?.owner_member_id ?? ''
  const [owner, setOwner] = useState(current)
  const selfID = useSelfID()
  const previous = members.find((m) => m.id === current)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const returnFocus = useReturnFocus()

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.workspaceOwnerSet(workspaceID, owner)
      await refreshWorkspaces(client)
      toast.success(`${workspace?.name ?? 'The workspace'} has a new owner`)
      onClose()
    } catch (err) {
      setError(message(err))
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-md" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Change owner of {workspace?.name ?? 'this workspace'}</DialogTitle>
          <DialogDescription>The owner administers the workspace. Only an admin or super admin can own one.</DialogDescription>
        </DialogHeader>
        <div className="flex min-w-0 flex-col gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <Label htmlFor="workspace-owner">Owner</Label>
            <Select value={owner} onValueChange={setOwner}>
              <SelectTrigger id="workspace-owner"><SelectValue placeholder="Choose an admin" /></SelectTrigger>
              <SelectContent>
                {members.map((m) => (
                  <SelectItem key={m.id} value={m.id} detail={roleLabel[m.role]}>
                    {ownerChoice(m, selfID, current)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {previous && owner !== '' && owner !== current && (
            <p className="text-ui-sm text-muted">
              {previous.id === selfID ? 'You keep' : `${previous.display_name} keeps`} access as {previous.role === 'super_admin' ? 'a super admin' : 'a granted member'}.
            </p>
          )}
          {error && <Callout tone="failed" role="alert">{error}</Callout>}
        </div>
        <DialogFooter>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button disabled={busy || owner === '' || owner === workspace?.owner_member_id} onClick={() => void save()}>
            {busy ? 'Saving…' : 'Change owner'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
