// The member roster: who is on the deployment, who is waiting on approval,
// and the admin verbs over both. Every mutation goes through the gateway and
// re-reads member.list afterwards; a refusal is the server's message, shown
// verbatim, never a prediction this view makes.

import { Copy, UserPlus } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { message } from '@/lib/format'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { ViewHeader } from '@/components/view-header'
import { api, type Api } from '@/lib/api'
import { timeAgo } from '@/lib/format'
import type { Member } from '@/lib/types'
import { registerRoute, type RouteProps } from '@/routes/registry'
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

// The house field style, duplicated per file rather than shared; see the
// admin dialogs for the other copies.
const field =
  'w-full rounded-md border bg-background px-2 py-1 text-sm outline-none focus-visible:ring-[2px] focus-visible:ring-ring/50'

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
  return (
    <div className="flex h-full flex-col">
      <ViewHeader
        title="Members"
        // A non-admin can read the roster but change nothing in it. Saying so
        // once here beats leaving them to infer it from absent buttons.
        subtitle={isAdmin ? count : `${count} - read only`}
      />
      <div className="flex-1 space-y-6 overflow-y-auto p-4">
        {caps.hasMethod('member.invite') && isAdmin && (
          <div>
            <Button size="sm" onClick={() => setInviting(true)}>
              <UserPlus />
              Invite
            </Button>
          </div>
        )}

        {pending.length > 0 && (
          <section aria-label="Pending members" className="space-y-2">
            <h2 className="text-xs font-medium text-muted-foreground">
              Waiting on approval
            </h2>
            <ul className="space-y-2">
              {pending.map((member) => (
                <li
                  key={member.id}
                  className="flex items-center gap-2 rounded-md border bg-card p-2 text-sm"
                >
                  <span className="min-w-0 flex-1 truncate">{member.display_name}</span>
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

        <section aria-label="Roster">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-xs text-muted-foreground">
                <th className="py-1 pr-2 font-normal">Member</th>
                <th className="py-1 pr-2 font-normal">Role</th>
                <th className="py-1 pr-2 font-normal">Presence</th>
                <th className="py-1 font-normal" />
              </tr>
            </thead>
            <tbody>
              {roster.map((member) => (
                <tr key={member.id} className="border-b last:border-b-0">
                  <td className="py-1.5 pr-2">
                    <span className="flex items-center gap-2">
                      <span
                        aria-hidden
                        className="size-2 shrink-0 rounded-full"
                        style={{ backgroundColor: member.color }}
                      />
                      <span className="truncate">{member.display_name}</span>
                      {member.id === self?.id && (
                        <span className="text-xs text-muted-foreground">(you)</span>
                      )}
                    </span>
                  </td>
                  <td className="py-1.5 pr-2 text-muted-foreground">
                    {caps.hasMethod('member.role') && isAdmin ? (
                      // No client-side prediction of who may be demoted: the
                      // server refuses to demote the last admin and says so,
                      // and that invariant is not recomputed here.
                      <select
                        className={field}
                        aria-label={`Role for ${member.display_name}`}
                        value={member.role}
                        onChange={(e) =>
                          changeRole(member, e.target.value as Member['role'])
                        }
                      >
                        {roles.map((role) => (
                          <option key={role} value={role}>
                            {role}
                          </option>
                        ))}
                      </select>
                    ) : (
                      member.role
                    )}
                  </td>
                  <td className="py-1.5 pr-2 text-muted-foreground">
                    {online.has(member.id)
                      ? 'online'
                      : lastSeen.has(member.id)
                        ? `offline - last seen ${timeAgo(lastSeen.get(member.id) ?? '')}`
                        : 'offline'}
                  </td>
                  <td className="py-1.5 text-right">
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
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>

        {self && caps.hasMethod('member.color') && (
          <section aria-label="Your color" className="space-y-2">
            <h2 className="text-xs font-medium text-muted-foreground">Your color</h2>
            <div className="flex gap-2">
              {presetColors.map((color) => (
                <button
                  key={color}
                  type="button"
                  aria-label={`Set color ${color}`}
                  aria-pressed={self.color === color}
                  className="size-6 rounded-full border ring-ring/50 hover:ring-[3px] aria-pressed:ring-[3px]"
                  style={{ backgroundColor: color }}
                  onClick={() => void recolor(color)}
                />
              ))}
            </div>
          </section>
        )}

        {error && <p className="text-xs text-state-failed">{error}</p>}
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
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Invite a member</DialogTitle>
          <DialogDescription>
            The code admits one join and is shown only here.
          </DialogDescription>
        </DialogHeader>
        {result ? (
          <div className="space-y-2">
            <div className="flex gap-2">
              <input
                ref={codeRef}
                readOnly
                aria-label="Invite code"
                className="w-full rounded-md border bg-background px-2 py-1 font-mono text-sm"
                value={result.code}
                onFocus={(e) => e.target.select()}
              />
              <Button variant="outline" size="sm" onClick={() => void copy()}>
                <Copy />
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              Expires {timeAgo(result.expires_at)}
            </p>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            Generate a code and hand it to the new member out of band.
          </p>
        )}
        {error && <p className="text-xs text-state-failed">{error}</p>}
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
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Remove {member.display_name}?</DialogTitle>
          <DialogDescription>
            Their runs and history stay; their access ends now.
          </DialogDescription>
        </DialogHeader>
        {error && <p className="text-xs text-state-failed">{error}</p>}
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
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Give up your admin role?</DialogTitle>
          <DialogDescription>
            You will become {role}. You will lose admin access immediately, and
            another admin has to hand it back.
          </DialogDescription>
        </DialogHeader>
        {error && <p className="text-xs text-state-failed">{error}</p>}
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
