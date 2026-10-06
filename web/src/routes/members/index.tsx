// A refusal is the server's message, shown verbatim, never a prediction this view makes.

import { Copy, UserPlus } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/lib/format'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { RelativeTime } from '@/components/ui/relative-time'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import type { Member } from '@/lib/types'
import { cn, field } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { MemberAvatar } from '@/routes/board/member-avatar'
import { InvitationsSection } from '@/routes/members/invitations'
import { PersonalSections } from '@/routes/members/personal'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import { onlineMembers } from '@/store/presence'

const roles: Member['role'][] = ['viewer', 'collaborator', 'admin']

export function MembersRoute({ client = api }: RouteProps & { client?: Api }) {
  const members = useStore((s) => s.members)
  const setMembers = useStore((s) => s.setMembers)
  const presence = useStore((s) => s.presence)
  const self = useStore((s) => s.info?.member)
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const [inviting, setInviting] = useState(false)
  const [removing, setRemoving] = useState<Member | null>(null)
  // The caller's own new role, held until they confirm the self-lockout.
  const [demoting, setDemoting] = useState<Member['role'] | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Approvals happen only here, so opening re-reads the hydrated roster.
  useEffect(() => {
    let cancelled = false
    client
      .memberList()
      .then((list) => {
        if (!cancelled) setMembers(list)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [client, setMembers])

  const refetch = async () => setMembers(await client.memberList())
  const approve = async (member: Member) => {
    setError(null)
    try {
      await client.memberApprove(member.id)
      await refetch()
      toast.success(`${member.display_name} approved`)
    } catch (err) {
      setError(message(err))
    }
  }

  const setRole = async (member: Member, role: Member['role']) => {
    setError(null)
    try {
      await client.memberRole(member.id, role)
      await refetch()
      toast.success(`${member.display_name} is now ${role}`)
    } catch (err) {
      setError(message(err))
    }
  }

  const changeRole = (member: Member, role: Member['role']) => {
    // Losing your own admin role locks you out at once; the server guards the rest.
    if (member.id === self?.id && role !== 'admin') {
      setDemoting(role)
      return
    }
    void setRole(member, role)
  }

  const online = new Set(onlineMembers(presence))
  const lastSeen = new Map(
    presence
      .filter((entry) => entry.state === 'offline')
      .map((entry) => [entry.member_id, entry.last_seen]),
  )
  const all = Object.values(members)
  const roster = all.filter((m) => !m.pending)
  const pending = all.filter((m) => m.pending)

  const count = roster.length === 1 ? '1 member' : `${roster.length} members`
  const canInvite = caps.hasMethod('member.invite') && isAdmin
  return (
    <div className="flex h-full min-h-0 flex-col">
      <ViewHeader
        title="Members"
        // Account sharing is self-service; only roster roles are read-only.
        subtitle={isAdmin ? count : `${count} - roles read only`}
        actions={
          canInvite ? (
            <Button size="md" onClick={() => setInviting(true)}>
              <UserPlus />
              Invite
            </Button>
          ) : undefined
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
          {error && (
            <p
              role="alert"
              className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed"
            >
              {error}
            </p>
          )}

          {pending.length > 0 && (
            <section aria-label="Pending members" className="space-y-2">
              <div className="flex min-h-[22px] items-center justify-between gap-2 border-b pb-1">
                <div className="min-w-0">
                  <h2 className="text-[13px] font-semibold">Waiting on approval</h2>
                  <p className="text-xs text-muted-foreground">
                    New members stay here until an admin approves them.
                  </p>
                </div>
                <Badge tone="needs-you">
                  {pending.length}
                </Badge>
              </div>
              <ul className="overflow-hidden border-y border-border">
                {pending.map((member) => (
                  <li
                    key={member.id}
                    className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 border-b border-border px-3 py-2 last:border-b-0"
                  >
                    <MemberAvatar member={member} fallback={member.display_name} className="size-7" />
                    <span className="min-w-0 break-words text-[13px] font-medium">
                      {member.display_name}
                    </span>
                    <span className="flex min-w-0 flex-wrap items-center justify-end gap-1.5">
                      <Badge tone="needs-you">
                        Pending
                      </Badge>
                      {caps.hasMethod('member.approve') && isAdmin && (
                        <Button size="md" onClick={() => void approve(member)}>
                          Approve
                        </Button>
                      )}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          )}

          <section aria-label="Roster" className="space-y-2">
            <div className="flex min-h-[22px] items-center justify-between gap-3 border-b pb-1">
              <h2 className="text-[13px] font-semibold">Roster</h2>
              <span className="text-xs text-muted-foreground">{roster.length} active</span>
            </div>
            <div className="overflow-hidden border-y border-border">
              <table className="w-full table-fixed text-sm">
                <thead className="hidden bg-sidebar md:table-header-group">
                  <tr className="text-left text-[11px] uppercase tracking-wide text-muted-foreground">
                    <th className="w-[35%] px-3 py-2 font-medium">Member</th>
                    <th className="w-[22%] px-3 py-2 font-medium">Role</th>
                    <th className="w-[28%] px-3 py-2 font-medium">Presence</th>
                    <th className="w-[15%] px-3 py-2 text-right font-medium">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {roster.map((member) => (
                    <tr key={member.id} className="block md:table-row">
                      <td className="block px-3 py-2.5 md:table-cell md:py-2">
                        <div className="flex min-w-0 items-center gap-3">
                          <MemberAvatar
                            member={member}
                            fallback={member.display_name}
                            className="size-7"
                          />
                          <span className="min-w-0 break-words font-medium">{member.display_name}</span>
                          {member.id === self?.id && (
                            <Badge>
                              (you)
                            </Badge>
                          )}
                        </div>
                      </td>
                      <td className="block px-3 py-2 md:table-cell md:py-2">
                        <div className="flex min-w-0 items-center justify-between gap-3 md:justify-start">
                          <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                            Role
                          </span>
                          {caps.hasMethod('member.role') && isAdmin ? (
                            // The server refuses to demote the last admin; not recomputed here.
                            <Select
                              value={member.role}
                              onValueChange={(role) =>
                                changeRole(member, role as Member['role'])
                              }
                            >
                              <SelectTrigger
                                id={`member-role-${member.id}`}
                                aria-label={`Role for ${member.display_name}`}
                                className={cn(field, 'w-full max-w-[12rem] md:w-44')}
                              >
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                {roles.map((role) => (
                                  <SelectItem key={role} value={role}>
                                    {role}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          ) : (
                            <Badge>
                              {member.role}
                            </Badge>
                          )}
                        </div>
                      </td>
                      <td className="block px-3 py-2 md:table-cell md:py-2">
                        <div className="flex min-w-0 items-center justify-between gap-3 md:justify-start">
                          <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                            Presence
                          </span>
                          {online.has(member.id) ? (
                            <Badge tone="done">
                              online
                            </Badge>
                          ) : (
                            <Badge className="max-w-full">
                              <span className="break-words">
                                {lastSeen.has(member.id)
                                  ? <>offline - last seen <RelativeTime at={lastSeen.get(member.id) ?? ''} /></>
                                  : 'offline'}
                              </span>
                            </Badge>
                          )}
                        </div>
                      </td>
                      <td
                        className={cn(
                          'block px-3 py-2.5 md:table-cell md:py-2 md:text-right',
                          !(caps.hasMethod('member.remove') &&
                            isAdmin &&
                            member.id !== self?.id) && 'hidden',
                        )}
                      >
                        {caps.hasMethod('member.remove') &&
                          isAdmin &&
                          member.id !== self?.id && (
                            <div className="flex min-w-0 items-center justify-between gap-3 md:justify-end">
                              <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                                Actions
                              </span>
                              <Button
                                size="md"
                                variant="ghost"
                                onClick={() => setRemoving(member)}
                              >
                                Remove
                              </Button>
                            </div>
                          )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          {isAdmin && caps.hasMethod('member.invitation.list') && (
            <InvitationsSection client={client} />
          )}

          <PersonalSections client={client} />
        </div>
      </div>

      {inviting && <InviteDialog client={client} onClose={() => setInviting(false)} />}
      {removing && (
        <RemoveDialog
          member={removing}
          client={client}
          onClose={() => setRemoving(null)}
          onRemoved={() => void refetch()}
        />
      )}
      {demoting && self && (
        <DemoteSelfDialog
          member={self}
          role={demoting}
          client={client}
          onClose={() => setDemoting(null)}
          onChanged={() => void refetch()}
        />
      )}
    </div>
  )
}

/**
 * The server shows the invite code exactly once. jsdom and older engines lack
 * navigator.clipboard, so the fallback selects the text.
 */
function InviteDialog({ client, onClose }: { client: Api; onClose: () => void }) {
  const [result, setResult] = useState<{ code: string; expires_at: string } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)
  const codeRef = useRef<HTMLInputElement>(null)

  const generate = async () => {
    setBusy(true)
    setError(null)
    try {
      setResult(await client.memberInvite())
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const copy = async () => {
    if (!result) return
    try {
      await navigator.clipboard.writeText(result.code)
      setCopied(true)
    } catch {
      codeRef.current?.focus()
      codeRef.current?.select()
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-h-[calc(100dvh-1rem)] sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Invite a member</DialogTitle>
          <DialogDescription>
            The code admits one join and is shown only here.
          </DialogDescription>
        </DialogHeader>
        {result ? (
          <div className="space-y-2">
            <div className="flex min-w-0 items-center gap-2 border-y border-border py-2">
              <Input
                ref={codeRef}
                readOnly
                aria-label="Invite code"
                className="min-w-0 flex-1 font-mono"
                value={result.code}
                onFocus={(e) => e.target.select()}
              />
              <Button variant="secondary" size="md" onClick={() => void copy()}>
                <Copy />
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              This code expires {timeAgo(result.expires_at)}. It can be used once.
            </p>
          </div>
        ) : (
          <div className="border-y border-dashed px-3 py-3">
            <p className="text-[13px] text-muted-foreground">
              Generate a code and hand it to the new member out of band.
            </p>
          </div>
        )}
        {error && (
          <p role="alert" className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="secondary" size="md" onClick={onClose}>
            {result ? 'Done' : 'Cancel'}
          </Button>
          {!result && (
            <Button size="md" disabled={busy} onClick={() => void generate()}>
              Generate code
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function RemoveDialog({
  member,
  client,
  onClose,
  onRemoved,
}: {
  member: Member
  client: Api
  onClose: () => void
  onRemoved: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const remove = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.memberRemove(member.id)
      onRemoved()
      onClose()
      toast.success(`${member.display_name} removed`)
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <AlertDialog
      open
      onOpenChange={() => {
        if (!busy) onClose()
      }}
    >
      <AlertDialogContent className="max-h-[calc(100dvh-1rem)] sm:max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>Remove {member.display_name}?</AlertDialogTitle>
          <AlertDialogDescription>
            Their runs and history stay; their access ends now.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void remove()
            }}
          >
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function DemoteSelfDialog({
  member,
  role,
  client,
  onClose,
  onChanged,
}: {
  member: Member
  role: Member['role']
  client: Api
  onClose: () => void
  onChanged: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const demote = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.memberRole(member.id, role)
      onChanged()
      onClose()
      toast.success(`You are now ${role}`)
    } catch (err) {
      setBusy(false)
      setError(message(err))
    }
  }

  return (
    <AlertDialog
      open
      onOpenChange={() => {
        if (!busy) onClose()
      }}
    >
      <AlertDialogContent className="max-h-[calc(100dvh-1rem)] sm:max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>Give up your admin role?</AlertDialogTitle>
          <AlertDialogDescription>
            You will become {role}. You will lose admin access immediately, and
            another admin has to hand it back.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              void demote()
            }}
          >
            Become {role}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

registerRoute('members', MembersRoute)
