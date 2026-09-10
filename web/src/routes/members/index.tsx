// The member roster: who is on the deployment, who is waiting on approval,
// and the admin verbs over both. Every mutation goes through the gateway and
// re-reads member.list afterwards; a refusal is the server's message, shown
// verbatim, never a prediction this view makes.

import { Copy, UserPlus } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Chip } from '@/components/ui/heroui'
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
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import type { Member } from '@/lib/types'
import { cn, field, focusRing } from '@/lib/utils'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { MemberAvatar } from '@/routes/board/member-avatar'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import { onlineMembers } from '@/store/presence'

// The attribution palette. These are colours, passed to member.color as
// data; the inline style below is the sanctioned member-colour exception.
const presetColors = [
  '#e6194b',
  '#3cb44b',
  '#f58231',
  '#911eb4',
  '#46f0f0',
  '#4363d8',
]

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
  // The role the caller picked for themselves, held until they confirm the
  // self-lockout; null when no such change is pending.
  const [demoting, setDemoting] = useState<Member['role'] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [sharedWith, setSharedWith] = useState<Member[]>([])
  const [sharing, setSharing] = useState<string | null>(null)

  // The roster the store holds came from hydration; this view is the one
  // place approvals happen, so opening it re-reads the list.
  useEffect(() => {
    let cancelled = false
    client
      .memberList()
      .then((list) => {
        if (!cancelled) setMembers(list)
      })
      .catch(() => {})
    client
      .accountList()
      .then((access) => {
        if (!cancelled) setSharedWith(access.shared_with)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [client, setMembers])

  const refetch = async () => setMembers(await client.memberList())
  const refetchShares = async () =>
    setSharedWith((await client.accountList()).shared_with)

  const toggleAccountShare = async (member: Member) => {
    setSharing(member.id)
    setError(null)
    try {
      const shared = sharedWith.some((entry) => entry.id === member.id)
      if (shared) {
        await client.accountRevoke(member.id)
      } else {
        await client.accountShare(member.id)
      }
      await refetchShares()
      toast.success(
        shared
          ? `Account access revoked from ${member.display_name}`
          : `Account shared with ${member.display_name}`,
      )
    } catch (err) {
      setError(message(err))
    } finally {
      setSharing(null)
    }
  }

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

  const recolor = async (color: string) => {
    setError(null)
    try {
      await client.memberColor(color)
      await refetch()
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
    // Losing your own admin role locks you out of this surface at once, so
    // it is the one change worth confirming. Everything else, including the
    // last-admin guard, is the server's refusal to make and ours to show.
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
            <Button size="sm" onClick={() => setInviting(true)}>
              <UserPlus />
              Invite
            </Button>
          ) : undefined
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <div className="mx-auto flex w-full max-w-6xl flex-col gap-5">
          {error && (
            <p
              role="alert"
              className="rounded-md border border-state-failed/30 bg-state-failed/10 px-3 py-2 text-sm text-state-failed"
            >
              {error}
            </p>
          )}

          {pending.length > 0 && (
            <section aria-label="Pending members" className="space-y-2">
              <div className="flex items-center justify-between gap-2">
                <div>
                  <h2 className="text-sm font-semibold">Waiting on approval</h2>
                  <p className="text-[13px] text-muted-foreground">
                    New members stay here until an admin approves them.
                  </p>
                </div>
                <Chip color="warning" variant="soft" size="sm">
                  <Chip.Label>{pending.length}</Chip.Label>
                </Chip>
              </div>
              <ul className="grid gap-2 sm:grid-cols-2">
                {pending.map((member) => (
                  <li
                    key={member.id}
                    className="flex min-w-0 items-center gap-3 rounded-lg border bg-card px-3 py-2.5 shadow-xs"
                  >
                    <MemberAvatar member={member} fallback={member.display_name} className="size-7" />
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">
                      {member.display_name}
                    </span>
                    <Chip color="warning" variant="tertiary" size="sm">
                      <Chip.Label>Pending</Chip.Label>
                    </Chip>
                    {caps.hasMethod('member.approve') && isAdmin && (
                      <Button size="sm" onClick={() => void approve(member)}>
                        Approve
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            </section>
          )}

          <section aria-label="Roster" className="space-y-2">
            <div className="flex items-end justify-between gap-3">
              <div>
                <h2 className="text-sm font-semibold">Roster</h2>
              </div>
              <span className="hidden text-[13px] text-muted-foreground sm:block">
                {roster.length} active
              </span>
            </div>
            <div className="overflow-hidden rounded-lg border bg-card shadow-xs">
              <table className="w-full text-sm">
                <thead className="hidden bg-muted/30 md:table-header-group">
                  <tr className="text-left text-[11px] uppercase tracking-wide text-muted-foreground">
                    <th className="w-[35%] px-3 py-2 font-medium">Member</th>
                    <th className="w-[22%] px-3 py-2 font-medium">Role</th>
                    <th className="w-[28%] px-3 py-2 font-medium">Presence</th>
                    <th className="px-3 py-2 text-right font-medium">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y">
                  {roster.map((member) => (
                    <tr key={member.id} className="block md:table-row">
                      <td className="block px-3 py-2.5 md:table-cell md:py-3">
                        <div className="flex min-w-0 items-center gap-3">
                          <MemberAvatar
                            member={member}
                            fallback={member.display_name}
                            className="size-7"
                          />
                          <span className="min-w-0 truncate font-medium">{member.display_name}</span>
                          {member.id === self?.id && (
                            <Chip color="accent" variant="tertiary" size="sm">
                              <Chip.Label>(you)</Chip.Label>
                            </Chip>
                          )}
                        </div>
                      </td>
                      <td className="block px-3 py-2 md:table-cell md:py-3">
                        <div className="flex items-center justify-between gap-3 md:justify-start">
                          <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                            Role
                          </span>
                          {caps.hasMethod('member.role') && isAdmin ? (
                            // No client-side prediction of who may be demoted: the
                            // server refuses to demote the last admin and says so,
                            // and that invariant is not recomputed here.
                            <Select
                              value={member.role}
                              onValueChange={(role) =>
                                changeRole(member, role as Member['role'])
                              }
                            >
                              <SelectTrigger
                                id={`member-role-${member.id}`}
                                aria-label={`Role for ${member.display_name}`}
                                className={cn(field, 'max-w-48 text-sm md:w-44')}
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
                            <Chip color="default" variant="tertiary" size="sm">
                              <Chip.Label>{member.role}</Chip.Label>
                            </Chip>
                          )}
                        </div>
                      </td>
                      <td className="block px-3 py-2 md:table-cell md:py-3">
                        <div className="flex items-center justify-between gap-3 md:justify-start">
                          <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                            Presence
                          </span>
                          {online.has(member.id) ? (
                            <Chip color="success" variant="soft" size="sm">
                              <Chip.Label>online</Chip.Label>
                            </Chip>
                          ) : (
                            <Chip color="default" variant="tertiary" size="sm">
                              <Chip.Label>
                                {lastSeen.has(member.id)
                                  ? `offline - last seen ${timeAgo(lastSeen.get(member.id) ?? '')}`
                                  : 'offline'}
                              </Chip.Label>
                            </Chip>
                          )}
                        </div>
                      </td>
                      <td className="block px-3 py-2.5 md:table-cell md:py-3 md:text-right">
                        <div className="flex items-center justify-between gap-3 md:justify-end">
                          <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground md:hidden">
                            Actions
                          </span>
                          {caps.hasMethod('member.remove') &&
                            isAdmin &&
                            member.id !== self?.id && (
                              <Button
                                size="sm"
                                variant="ghost"
                                onClick={() => setRemoving(member)}
                              >
                                Remove
                              </Button>
                            )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          {self &&
            caps.hasMethod('account.share') &&
            caps.hasMethod('account.revoke') && (
              <section aria-label="Account sharing" className="space-y-3">
                <div>
                  <h2 className="text-sm font-semibold">Your agent account</h2>
                  <p className="mt-1 max-w-2xl text-[13px] leading-5 text-muted-foreground">
                    Choose teammates who may launch runs with your account. This is
                    separate from roster roles and can be changed at any time.
                  </p>
                </div>
                <div className="rounded-lg border bg-card p-3 shadow-xs">
                  <ul className="mb-3 grid gap-1 text-[13px] text-muted-foreground sm:grid-cols-2">
                    <li>Saved environment, agent login, profile, and vendor quota are shared.</li>
                    <li>Their runs remain attributed to them; running agents are not stopped.</li>
                  </ul>
                  <ul className="divide-y">
                    {roster
                      .filter((member) => member.id !== self.id)
                      .map((member) => {
                        const shared = sharedWith.some(
                          (entry) => entry.id === member.id,
                        )
                        return (
                          <li
                            key={member.id}
                            className="flex min-w-0 items-center gap-3 py-2.5 first:pt-0 last:pb-0"
                          >
                            <MemberAvatar
                              member={member}
                              fallback={member.display_name}
                              className="size-6"
                            />
                            <span className="min-w-0 flex-1 truncate text-sm font-medium">
                              {member.display_name}
                            </span>
                            <Chip
                              color={shared ? 'success' : 'default'}
                              variant="tertiary"
                              size="sm"
                              className="hidden sm:flex"
                            >
                              <Chip.Label>{shared ? 'Shared' : 'Not shared'}</Chip.Label>
                            </Chip>
                            <Button
                              size="sm"
                              variant={shared ? 'outline' : 'default'}
                              disabled={sharing !== null}
                              onClick={() => void toggleAccountShare(member)}
                            >
                              {sharing === member.id
                                ? 'Saving...'
                                : shared
                                  ? 'Revoke access'
                                  : 'Share account'}
                            </Button>
                          </li>
                        )
                      })}
                  </ul>
                </div>
              </section>
            )}

          {self && caps.hasMethod('member.color') && (
            <section aria-label="Your color" className="space-y-3">
              <div>
                <h2 className="text-sm font-semibold">Your color</h2>
                <p className="mt-1 text-[13px] text-muted-foreground">
                  Used for your presence and activity attribution.
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-card p-3 shadow-xs">
                {presetColors.map((color) => (
                  <button
                    key={color}
                    type="button"
                    aria-label={`Set color ${color}`}
                    aria-pressed={self.color === color}
                    title={self.color === color ? `Current color ${color}` : `Set color ${color}`}
                    className={cn(
                      focusRing,
                      'size-8 rounded-full border-2 border-background shadow-xs ring-1 ring-border/70 transition-transform hover:scale-105 hover:ring-2 aria-pressed:ring-2',
                    )}
                    style={{ backgroundColor: color }}
                    onClick={() => void recolor(color)}
                  />
                ))}
                <Chip color="default" variant="tertiary" size="sm" className="ml-1">
                  <Chip.Label>{self.color}</Chip.Label>
                </Chip>
              </div>
            </section>
          )}
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
 * member.invite mints a one-time code the server shows exactly once, so the
 * dialog holds it until dismissed and offers the clipboard. jsdom and older
 * engines have no navigator.clipboard; the fallback selects the text.
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
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Invite a member</DialogTitle>
          <DialogDescription>
            The code admits one join and is shown only here.
          </DialogDescription>
        </DialogHeader>
        {result ? (
          <div className="space-y-3">
            <div className="flex items-center gap-2 rounded-md bg-muted/35 p-2">
              <Input
                ref={codeRef}
                readOnly
                aria-label="Invite code"
                className="min-w-0 flex-1 font-mono"
                value={result.code}
                onFocus={(e) => e.target.select()}
              />
              <Button variant="outline" size="sm" onClick={() => void copy()}>
                <Copy />
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
            <p className="text-[13px] text-muted-foreground">
              This code expires {timeAgo(result.expires_at)}. It can be used once.
            </p>
          </div>
        ) : (
          <div className="rounded-md border border-dashed px-3 py-4">
            <p className="text-sm text-muted-foreground">
              Generate a code and hand it to the new member out of band.
            </p>
          </div>
        )}
        {error && (
          <p role="alert" className="rounded-md bg-state-failed/10 px-3 py-2 text-sm text-state-failed">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {result ? 'Done' : 'Cancel'}
          </Button>
          {!result && (
            <Button disabled={busy} onClick={() => void generate()}>
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
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Remove {member.display_name}?</DialogTitle>
          <DialogDescription>
            Their runs and history stay; their access ends now.
          </DialogDescription>
        </DialogHeader>
        {error && (
          <p role="alert" className="rounded-md bg-state-failed/10 px-3 py-2 text-sm text-state-failed">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="destructive" disabled={busy} onClick={() => void remove()}>
            Remove
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * Giving up your own admin role is the one member change worth confirming:
 * it takes effect at once and no other affordance here can undo it. Any
 * other refusal, the last-admin guard included, arrives from the server.
 */
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
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Give up your admin role?</DialogTitle>
          <DialogDescription>
            You will become {role}. You will lose admin access immediately, and
            another admin has to hand it back.
          </DialogDescription>
        </DialogHeader>
        {error && (
          <p role="alert" className="rounded-md bg-state-failed/10 px-3 py-2 text-sm text-state-failed">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="destructive" disabled={busy} onClick={() => void demote()}>
            Become {role}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

registerRoute('members', MembersRoute)
