import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Ellipsis } from '@/components/icons'
import { Confirm } from '@/components/confirm'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Menu, MenuContent, MenuItem, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { StatusDot } from '@/components/ui/status-dot'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AccountAccess, Member } from '@/lib/types'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import { onlineMembers } from '@/store/presence'

export const roles: Member['role'][] = ['viewer', 'collaborator', 'admin']

export const roleLabel: Record<Member['role'], string> = {
  viewer: 'Viewer',
  collaborator: 'Collaborator',
  admin: 'Admin',
}

function accountWords(id: string, access: AccountAccess | null): string {
  if (!access) return ''
  const theirs = access.accounts.some((m) => m.id === id)
  const mine = access.shared_with.some((m) => m.id === id)
  if (theirs && mine) return 'Shared both ways'
  if (theirs) return 'Shares with you'
  if (mine) return 'You share yours'
  return 'Not shared'
}

export function Roster({ client }: { client: Api }) {
  const members = useStore((s) => s.members)
  const setMembers = useStore((s) => s.setMembers)
  const presence = useStore((s) => s.presence)
  const self = useStore((s) => s.info?.member)
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const [access, setAccess] = useState<AccountAccess | null>(null)
  const [removing, setRemoving] = useState<Member | null>(null)
  const [demoting, setDemoting] = useState<Member['role'] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const canListAccounts = caps.hasMethod('account.list')

  useEffect(() => {
    let live = true
    client.memberList().then((list) => live && setMembers(list), () => {})
    if (canListAccounts) client.accountList().then((a) => live && setAccess(a), () => {})
    return () => {
      live = false
    }
  }, [canListAccounts, client, setMembers])

  const refetch = async () => setMembers(await client.memberList())

  const act = async (work: () => Promise<unknown>, done: string) => {
    setError(null)
    try {
      await work()
      await refetch()
      toast.success(done)
    } catch (err) {
      setError(message(err))
    }
  }

  const changeRole = (member: Member, role: Member['role']) => {
    if (role === member.role) return
    // Losing your own admin role locks you out at once; the server guards the rest.
    if (member.id === self?.id && role !== 'admin') {
      setDemoting(role)
      return
    }
    void act(() => client.memberRole(member.id, role), `${member.display_name} is now ${roleLabel[role].toLowerCase()}`)
  }

  const online = new Set(onlineMembers(presence))
  const lastSeen = new Map(presence.map((entry) => [entry.member_id, entry.last_seen]))
  const all = Object.values(members)
  const roster = all.filter((m) => !m.pending)
  const pending = all.filter((m) => m.pending)
  const canRole = isAdmin && caps.hasMethod('member.role')
  const canRemove = isAdmin && caps.hasMethod('member.remove')
  const canApprove = isAdmin && caps.hasMethod('member.approve')

  return (
    <>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      {pending.length > 0 && (
        <section aria-label="Pending members" className="flex min-w-0 flex-col gap-2">
          <SectionLabel as="h2">Waiting for approval</SectionLabel>
          <ul className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
            {pending.map((member) => (
              <li key={member.id} className="flex min-h-11 min-w-0 items-center gap-3 px-3 py-2">
                <Avatar name={member.display_name} color={member.color} size="header" />
                <span className="min-w-0 flex-1 truncate text-ui font-medium text-text">{member.display_name}</span>
                {canApprove
                  ? <Button size="sm" onClick={() => void act(() => client.memberApprove(member.id), `${member.display_name} approved`)}>Approve</Button>
                  : <span className="text-ui-sm text-muted">Waiting for an admin</span>}
              </li>
            ))}
          </ul>
        </section>
      )}
      <section aria-label="Roster" className="flex min-w-0 flex-col gap-2">
        <SectionLabel as="h2">{roster.length === 1 ? '1 member' : `${roster.length} members`}</SectionLabel>
        <div className="overflow-hidden rounded-panel border border-seam">
          <table className="w-full table-fixed text-ui">
            <thead className="border-b border-seam bg-chrome text-left text-ui-sm text-muted">
              <tr>
                <th className="px-3 py-1.5 font-medium">Name</th>
                <th className="w-32 px-3 py-1.5 font-medium">Role</th>
                <th className="hidden w-40 px-3 py-1.5 font-medium lg:table-cell">Agent account</th>
                <th className="hidden w-36 px-3 py-1.5 font-medium sm:table-cell">Last seen</th>
                {canRole && <th className="w-12 px-1 py-1.5"><span className="sr-only">Actions</span></th>}
              </tr>
            </thead>
            <tbody className="divide-y divide-seam">
              {roster.map((member) => {
                const you = member.id === self?.id
                const seen = lastSeen.get(member.id)
                return (
                  <tr key={member.id}>
                    <td className="px-3 py-2">
                      <span className="flex min-w-0 items-center gap-2">
                        <Avatar name={member.display_name} color={member.color} size="header" />
                        <span className="min-w-0 truncate font-medium text-text">{member.display_name}</span>
                        {you && <span className="shrink-0 text-ui-sm text-muted">(you)</span>}
                      </span>
                    </td>
                    <td className="px-3 py-2 text-text">{roleLabel[member.role]}</td>
                    <td className="hidden px-3 py-2 text-muted lg:table-cell">{you ? '' : accountWords(member.id, access)}</td>
                    <td className="hidden px-3 py-2 text-muted sm:table-cell">
                      {online.has(member.id)
                        ? <span className="flex items-center gap-1.5 text-text"><StatusDot tone="done" />Online</span>
                        : seen ? <RelativeTime at={seen} /> : 'Offline'}
                    </td>
                    {canRole && (
                      <td className="px-1 py-1 text-right">
                        <Menu>
                          <MenuTrigger asChild>
                            <Button variant="ghost" size="icon" label={`Actions for ${member.display_name}`}>
                              <Ellipsis />
                            </Button>
                          </MenuTrigger>
                          <MenuContent align="end">
                            <MenuLabel>Role</MenuLabel>
                            <MenuRadioGroup value={member.role} onValueChange={(role) => changeRole(member, role as Member['role'])}>
                              {roles.map((role) => <MenuRadioItem key={role} value={role}>{roleLabel[role]}</MenuRadioItem>)}
                            </MenuRadioGroup>
                            {canRemove && !you && (
                              <>
                                <MenuSeparator />
                                <MenuItem onSelect={() => setRemoving(member)}>Remove…</MenuItem>
                              </>
                            )}
                          </MenuContent>
                        </Menu>
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
          description="Their runs and history stay; their access ends now."
          action="Remove"
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
          title="Give up your admin role?"
          description={`You become ${roleLabel[demoting].toLowerCase()} and lose admin access at once. Another admin has to hand it back.`}
          action={`Become ${roleLabel[demoting].toLowerCase()}`}
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
