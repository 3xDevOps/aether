import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Search } from '@/components/icons'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import { isAdminRole } from '@/lib/permissions'
import { cn } from '@/lib/utils'
import type { Member, WorkspaceAccess } from '@/lib/types'
import { roleLabel } from '@/routes/members/roster'
import { sameIdentity, useStore } from '@/store'
import { useIsSuperAdmin } from '@/store/hooks'

async function refreshWorkspaces(client: Api) {
  const same = sameIdentity()
  const list = await client.workspaceListFull()
  if (same()) useStore.getState().setWorkspaces(list)
}

function useMembers(client: Api): [Member[], string | null] {
  const members = useStore((s) => s.members)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    let live = true
    const same = sameIdentity()
    useStore.getState().readMembers(client.memberList()).then(
      (list) => live && same() && useStore.getState().setMemberList(list),
      (err: unknown) => live && same() && setError(`Could not list members: ${message(err)}`),
    )
    return () => {
      live = false
    }
  }, [client])
  return [Object.values(members).filter((m) => !m.pending), error]
}

function useCloseOnIdentityChange(onClose: () => void) {
  const identity = useStore((s) => s.identityKey)
  const opened = useRef(identity)
  useEffect(() => {
    if (identity !== opened.current) onClose()
  }, [identity, onClose])
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

export function WorkspaceAccessDialog({ workspaceID, client, onClose, onChangeOwner }: { workspaceID: string; client: Api; onClose: () => void; onChangeOwner?: () => void }) {
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const superAdmin = useIsSuperAdmin()
  const [members, membersError] = useMembers(client)
  const byID = new Map(members.map((m) => [m.id, m]))
  const [access, setAccess] = useState<WorkspaceAccess | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set())
  const [query, setQuery] = useState('')
  const selfID = useSelfID()
  const returnFocus = useReturnFocus()
  useCloseOnIdentityChange(onClose)

  const load = useCallback(async () => {
    const same = sameIdentity()
    try {
      const list = await client.workspaceAccessList(workspaceID)
      if (same()) setAccess(list)
    } catch (err) {
      if (same()) setError(message(err))
    }
  }, [client, workspaceID])

  useEffect(() => {
    void load()
  }, [load])

  const where = workspace?.name ?? 'the workspace'
  const turn = async (member: Member, on: boolean) => {
    setBusy((ids) => new Set(ids).add(member.id))
    setError(null)
    try {
      await (on ? client.workspaceAccessGrant(workspaceID, [member.id]) : client.workspaceAccessRevoke(workspaceID, member.id))
      toast.success(on ? `${member.display_name} now reaches ${where}` : `${member.display_name} no longer reaches ${where}`)
      await Promise.all([load(), refreshWorkspaces(client)])
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy((ids) => new Set([...ids].filter((id) => id !== member.id)))
    }
  }

  const owner = access ? byID.get(access.owner_member_id) : undefined
  const granted = new Set(access?.grants.map((g) => g.member_id))
  const superAdmins = members.filter((m) => m.role === 'super_admin' && m.id !== access?.owner_member_id)
  const people = members.filter((m) => m.role !== 'super_admin' && m.id !== access?.owner_member_id)
  const needle = query.trim().toLowerCase()
  const shown = people.filter((m) => m.display_name.toLowerCase().includes(needle))
  const reaching = people.filter((m) => granted.has(m.id)).length

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-lg" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Access to {workspace?.name ?? 'this workspace'}</DialogTitle>
          <DialogDescription>
            Tick each member who may use this workspace. Each change is saved at once. A granted admin works here as a collaborator.
          </DialogDescription>
        </DialogHeader>
        <div className="flex min-w-0 flex-col gap-3">
          {membersError && <Callout tone="failed" role="alert">{membersError}</Callout>}
          {error && <Callout tone="failed" role="alert">{error}</Callout>}
          {!access && !error && (
            <div role="status" aria-label="Loading access" className="flex flex-col gap-0.5">
              {[0, 1, 2].map((row) => <Skeleton key={row} className="h-9" />)}
            </div>
          )}
          {access && (
            <>
              <InsetList aria-label="Owner">
                <InsetListItem>
                  <MemberLine member={owner} id={access.owner_member_id} detail={owner ? `Owner · ${roleLabel[owner.role]}` : 'Owner'} selfID={selfID} />
                  {onChangeOwner && <Button size="sm" variant="secondary" onClick={onChangeOwner}>Change owner…</Button>}
                </InsetListItem>
              </InsetList>
              <div className="relative flex items-center">
                <Search aria-hidden className="pointer-events-none absolute left-2 size-3.5 text-muted" />
                <Input
                  type="search"
                  aria-label="Search members"
                  placeholder="Search members"
                  className="pl-7"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </div>
              <div className="flex items-baseline justify-between gap-3 text-ui-sm text-muted">
                <span>{people.length === 0 ? 'No other members yet' : `${reaching} of ${people.length} ${people.length === 1 ? 'member has' : 'members have'} access`}</span>
              </div>
              {shown.length > 0 && (
                <InsetList aria-label="Members" className="max-h-80 overflow-y-auto">
                  {shown.map((member) => {
                    const id = `access-${member.id}`
                    // An admin grants and revokes only members below admin; the server holds the same rule.
                    const locked = !superAdmin && isAdminRole(member.role)
                    return (
                      <InsetListItem key={member.id}>
                        <Checkbox
                          id={id}
                          checked={granted.has(member.id)}
                          disabled={locked || busy.has(member.id)}
                          onCheckedChange={(on) => void turn(member, on === true)}
                        />
                        <label htmlFor={id} className={cn('flex min-w-0 flex-1 items-center gap-3', locked ? 'cursor-not-allowed' : 'cursor-pointer')}>
                          <MemberLine member={member} id={member.id} detail={locked ? `${roleLabel[member.role]} · only a super admin changes an admin's access` : grantedRole(member)} selfID={selfID} />
                        </label>
                      </InsetListItem>
                    )
                  })}
                </InsetList>
              )}
              {people.length > 0 && shown.length === 0 && <p className="text-ui-sm text-muted">No member matches &ldquo;{query.trim()}&rdquo;.</p>}
              <p className="text-ui-sm text-muted">
                {superAdmins.length > 0 && `Super admins reach every workspace: ${superAdmins.map((m) => m.display_name).join(', ')}. `}
                {superAdmin ? 'Invite someone new from Members.' : 'Invite someone new from Members, or ask a super admin to add a member you do not see here.'}
              </p>
            </>
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
  const [allMembers, membersError] = useMembers(client)
  const members = allMembers.filter((m) => isAdminRole(m.role))
  const current = workspace?.owner_member_id ?? ''
  const [owner, setOwner] = useState(current)
  const selfID = useSelfID()
  const previous = members.find((m) => m.id === current)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const returnFocus = useReturnFocus()
  useCloseOnIdentityChange(onClose)

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      const same = sameIdentity()
      await client.workspaceOwnerSet(workspaceID, owner)
      await refreshWorkspaces(client)
      if (!same()) return
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
          {membersError && <Callout tone="failed" role="alert">{membersError}</Callout>}
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
