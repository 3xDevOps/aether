import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Trash2 } from '@/components/icons'
import { Confirm } from '@/components/confirm'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { FailureCallout } from '@/components/ui/failure-callout'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { StatusDot } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { useMediaQuery } from '@/lib/hooks'
import type { Member } from '@/lib/types'
import { AccountSharingNote, useAccountSharing } from '@/routes/members/account-sharing'
import { useFailure } from '@/components/failure'
import { useStore } from '@/store'
import { nextMemberOrder, type MemberPatch } from '@/store/members'
import { useCapability, useIsAdmin, useIsSuperAdmin, useWorkspaceAccessServer } from '@/store/hooks'
import { optimistic } from '@/store/optimistic'
import { onlineMembers } from '@/store/presence'

export const roles: Member['role'][] = ['viewer', 'collaborator', 'admin', 'super_admin']

export const roleLabel: Record<Member['role'], string> = {
  viewer: 'Viewer',
  collaborator: 'Collaborator',
  admin: 'Admin',
  super_admin: 'Super admin',
}

export const roleDescription: Record<Member['role'], string> = {
  viewer: 'Reads the workspaces they are granted',
  collaborator: 'Works in the workspaces they are granted',
  admin: 'Owns and runs their own workspaces',
  super_admin: 'Every workspace, members and server settings',
}

export function Roster({ client }: { client: Api }) {
  const members = useStore((s) => s.members)
  const setMemberList = useStore((s) => s.setMemberList)
  const presence = useStore((s) => s.presence)
  const self = useStore((s) => s.info?.member)
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const superAdmin = useIsSuperAdmin()
  const scoped = useWorkspaceAccessServer()
  const [removing, setRemoving] = useState<Member | null>(null)
  const [demoting, setDemoting] = useState<Member['role'] | null>(null)
  const { failures, run } = useFailure()
  const sharing = useAccountSharing(client)
  // Under 48rem the share control sits below the name, so the table never scrolls sideways.
  const wide = useMediaQuery('(min-width: 48rem)')
  const canShare = caps.hasMethod('account.share') && caps.hasMethod('account.revoke')

  const readMembers = useCallback(() => useStore.getState().readMembers(client.memberList()), [client])

  useEffect(() => {
    let live = true
    readMembers().then((list) => live && setMemberList(list), () => {})
    return () => {
      live = false
    }
  }, [readMembers, setMemberList])

  const refetch = async () => setMemberList(await readMembers())

  const update = (member: Member, next: Partial<Member>, send: () => Promise<Member>, done: string) => {
    const { patchMember, revertMember, confirmMember } = useStore.getState()
    let patch: MemberPatch | undefined
    const order = nextMemberOrder()
    void run((answered) => optimistic({
      apply: () => {
        patch = patchMember(member.id, next, order)
      },
      send,
      confirm: (result) => {
        answered()
        confirmMember(member.id, Object.fromEntries(Object.keys(next).map((key) => [key, result[key as keyof Member]])), patch)
      },
      revert: () => revertMember(member.id, patch),
    }), { onDone: () => toast.success(done), key: member.id })
  }

  const changeRole = (member: Member, role: Member['role']) => {
    if (role === member.role) return
    // Losing your own super admin role locks you out at once; the server guards the rest.
    if (member.id === self?.id && role !== 'super_admin') {
      setDemoting(role)
      return
    }
    update(member, { role }, () => client.memberRole(member.id, role), `${member.display_name} is now ${roleLabel[role].toLowerCase()}`)
  }

  const online = new Set(onlineMembers(presence))
  const lastSeen = new Map(presence.map((entry) => [entry.member_id, entry.last_seen]))
  const all = Object.values(members)
  const roster = all.filter((m) => !m.pending)
  const pending = all.filter((m) => m.pending)
  const canRole = superAdmin && caps.hasMethod('member.role')
  const canRemove = superAdmin && caps.hasMethod('member.remove')
  const canApprove = isAdmin && caps.hasMethod('member.approve')

  return (
    <>
      {failures.map((failure) => <FailureCallout key={failure.id} error={failure.error} onRetry={failure.retry} />)}
      {pending.length > 0 && (
        <section aria-label="Pending members" className="flex min-w-0 flex-col gap-2">
          <SectionLabel as="h2">Waiting for approval</SectionLabel>
          <InsetList>
            {pending.map((member) => (
              <InsetListItem key={member.id}>
                <Avatar name={member.display_name} color={member.color} size="header" />
                <span className="min-w-0 flex-1 truncate text-ui font-medium text-text">{member.display_name}</span>
                {canApprove
                  ? <Button size="sm" onClick={() => update(member, { pending: false }, () => client.memberApprove(member.id), `${member.display_name} approved`)}>Approve</Button>
                  : <span className="text-ui-sm text-muted">Waiting for an admin</span>}
              </InsetListItem>
            ))}
          </InsetList>
        </section>
      )}
      <section aria-label="Roster" className="flex min-w-0 flex-col gap-2">
        <SectionLabel as="h2">{roster.length === 1 ? '1 member' : `${roster.length} members`}</SectionLabel>
        {!superAdmin && <p className="text-ui-sm text-muted">You see super admins and the members of workspaces you can use.</p>}
        {canShare && <AccountSharingNote />}
        {sharing.notices}
        <div className="overflow-hidden rounded-panel border border-seam">
          <table className="w-full table-fixed text-ui">
            <thead className="border-b border-seam bg-chrome text-left text-ui-sm text-muted">
              <tr>
                <th className="px-3 py-1.5 font-medium">Name</th>
                <th className="w-36 px-3 py-1.5 font-medium md:w-40">Role</th>
                {wide && <th className="w-48 px-3 py-1.5 font-medium">Agent account</th>}
                <th className="hidden w-32 px-3 py-1.5 font-medium md:table-cell">Last seen</th>
                {canRemove && <th className="w-11 px-1 py-1.5"><span className="sr-only">Remove</span></th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-seam">
              {roster.map((member) => {
                const you = member.id === self?.id
                const seen = lastSeen.get(member.id)
                const presence = online.has(member.id)
                  ? <span className="flex items-center gap-1.5 text-text"><StatusDot tone="done" />Online</span>
                  : seen ? <RelativeTime at={seen} /> : 'Offline'
                const account = !you && (
                  <span className="flex min-w-0 flex-col items-start gap-1">
                    {canShare && (
                      <Button
                        size="sm"
                        variant="secondary"
                        aria-label={sharing.shares(member.id) ? `Stop sharing your agent account with ${member.display_name}` : `Share your agent account with ${member.display_name}`}
                        disabled={sharing.pending.has(member.id)}
                        onClick={() => sharing.toggle(member)}
                      >
                        {sharing.shares(member.id) ? 'Stop sharing' : 'Share my account'}
                      </Button>
                    )}
                    {sharing.sharesWithMe(member.id) && <span className="text-ui-sm text-muted">Shares theirs with you</span>}
                  </span>
                )
                return (
                  <tr key={member.id} className="transition-colors hover:bg-hover motion-reduce:transition-none">
                    <td className="px-3 py-2">
                      <span className="flex min-w-0 items-center gap-2">
                        <Avatar name={member.display_name} color={member.color} size="header" />
                        <span className="flex min-w-0 flex-col">
                          <span className="flex min-w-0 items-baseline gap-1">
                            <span className="min-w-0 truncate font-medium text-text">{member.display_name}</span>
                            {you && <span className="shrink-0 text-ui-sm text-muted">(you)</span>}
                          </span>
                          <span className="text-ui-sm text-muted md:hidden">{presence}</span>
                        </span>
                      </span>
                      {!wide && account && <div className="pt-2">{account}</div>}
                    </td>
                    <td className="px-3 py-2">
                      {canRole ? (
                        <Select value={member.role} onValueChange={(role) => changeRole(member, role as Member['role'])}>
                          <SelectTrigger aria-label={`Role of ${member.display_name}`} className="h-7 w-full">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {roles.filter((role) => scoped || role !== 'super_admin').map((role) => (
                              <SelectItem key={role} value={role} description={roleDescription[role]}>{roleLabel[role]}</SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      ) : <span className="text-text">{roleLabel[member.role]}</span>}
                    </td>
                    {wide && <td className="px-3 py-2">{account}</td>}
                    <td className="hidden px-3 py-2 text-muted md:table-cell">{presence}</td>
                    {canRemove && (
                      <td className="px-1 py-1 text-right">
                        {!you && (
                          <Button variant="ghost" size="icon" label={`Remove ${member.display_name}`} onClick={() => setRemoving(member)}>
                            <Trash2 />
                          </Button>
                        )}
                      </td>
                    )}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </section>
      {removing && (
        <Confirm
          title={`Remove ${removing.display_name}?`}
          description="Their sessions and history stay; their access ends now."
          action="Remove"
          pending="Removing…"
          onConfirm={() => client.memberRemove(removing.id)}
          onDone={() => {
            toast.success(`${removing.display_name} removed`)
            void refetch()
          }}
          onClose={() => setRemoving(null)}
        />
      )}
      {demoting && self && (
        <Confirm
          title="Give up your super admin role?"
          description={`You become ${roleLabel[demoting].toLowerCase()} and lose server-wide admin access at once. Another super admin has to hand it back.`}
          action={`Become ${roleLabel[demoting].toLowerCase()}`}
          pending={`Becoming ${roleLabel[demoting].toLowerCase()}…`}
          onConfirm={() => client.memberRole(self.id, demoting)}
          onDone={() => {
            toast.success(`You are now ${roleLabel[demoting].toLowerCase()}`)
            void refetch()
          }}
          onClose={() => setDemoting(null)}
        />
      )}
    </>
  )
}
