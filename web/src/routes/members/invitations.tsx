// Invitations for edge accounts: an admin names the GitHub login or email a
// person signs in to the edge with. No code changes hands, unlike the Invite
// button's one-time codes for SSH-key joins.

import { useEffect, useState } from 'react'
import { toast } from 'sonner'
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
import { Chip } from '@/components/ui/heroui'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { Api } from '@/lib/api'
import { message, providerName, timeAgo } from '@/lib/format'
import type { Invitation, Member } from '@/lib/types'
import { cn, field } from '@/lib/utils'
import { useStore } from '@/store'

const roles: Member['role'][] = ['viewer', 'collaborator', 'admin']

/** Who an invitation admits, as the person reading the list knows them. */
function invitee(invitation: Invitation): string {
  if (invitation.login) return `${invitation.login} on GitHub`
  const provider = invitation.provider ? providerName[invitation.provider] : undefined
  return provider ? `${invitation.email} on ${provider}` : (invitation.email ?? '')
}

export function InvitationsSection({ client }: { client: Api }) {
  const members = useStore((s) => s.members)
  const [invitations, setInvitations] = useState<Invitation[] | null>(null)
  const [by, setBy] = useState<'login' | 'email'>('login')
  const [who, setWho] = useState('')
  const [role, setRole] = useState<Member['role']>('collaborator')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<Invitation | null>(null)

  useEffect(() => {
    let live = true
    client.memberInvitationList().then(
      (list) => {
        if (live) setInvitations(list)
      },
      (err: unknown) => {
        if (live) setError(message(err))
      },
    )
    return () => {
      live = false
    }
  }, [client])

  const refetch = async () => setInvitations(await client.memberInvitationList())

  const invite = async () => {
    setBusy(true)
    setError(null)
    try {
      const invitation = await client.memberInvitationCreate(
        by === 'login'
          ? { provider: 'github', login: who.trim(), role }
          : { email: who.trim(), role },
      )
      setWho('')
      toast.success(`Invited ${invitee(invitation)}`)
      await refetch()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const memberName = (id: string) => members[id]?.display_name ?? id

  return (
    <section aria-label="Invitations" className="space-y-2">
      <div className="min-w-0 border-b pb-1">
        <h2 className="text-[13px] font-semibold">Invitations</h2>
        <p className="mt-1 max-w-2xl text-xs leading-4 text-muted-foreground">
          Invite a person by the account they sign in to the edge with. After aether login they
          see this server. When signing in is enough for this server, their first connection
          makes them a member with the role you pick. When the server admits approved devices
          only, their device waits for an admin to approve it with the code it shows, using{' '}
          <span className="font-mono">aether device approve</span> or{' '}
          <span className="font-mono">sudo aether-server device approve</span> on the server;
          approving it makes them a member. Invitations expire after 7 days.
        </p>
      </div>

      <form
        aria-label="Invite an account"
        className="grid min-w-0 gap-2 sm:grid-cols-[10rem_minmax(0,1fr)_10rem_auto] sm:items-end"
        onSubmit={(e) => {
          e.preventDefault()
          void invite()
        }}
      >
        <div className="min-w-0 space-y-1 text-sm">
          <Label htmlFor="invitation-by">Invite by</Label>
          <Select value={by} onValueChange={(value) => setBy(value as 'login' | 'email')}>
            <SelectTrigger id="invitation-by" className={cn(field, 'w-full')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="login">GitHub login</SelectItem>
              <SelectItem value="email">Email</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <Label className="block min-w-0 space-y-1">
          {by === 'login' ? 'GitHub login' : 'Email'}
          <Input
            className="min-w-0"
            type={by === 'email' ? 'email' : 'text'}
            autoComplete="off"
            placeholder={by === 'login' ? 'octocat' : 'dev@example.com'}
            value={who}
            disabled={busy}
            onChange={(e) => setWho(e.target.value)}
          />
        </Label>
        <div className="min-w-0 space-y-1 text-sm">
          <Label htmlFor="invitation-role">Role</Label>
          <Select value={role} onValueChange={(value) => setRole(value as Member['role'])}>
            <SelectTrigger id="invitation-role" className={cn(field, 'w-full')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {roles.map((r) => (
                <SelectItem key={r} value={r}>
                  {r}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button type="submit" size="default" disabled={busy || !who.trim()}>
          Invite account
        </Button>
      </form>

      {error && (
        <p
          role="alert"
          className="border-l-2 border-state-failed bg-state-failed/10 px-3 py-2 text-[13px] text-state-failed"
        >
          {error}
        </p>
      )}

      {invitations?.length === 0 && (
        <p className="border-y border-border px-3 py-2 text-xs text-muted-foreground">
          No open invitations.
        </p>
      )}
      {invitations && invitations.length > 0 && (
        <ul aria-label="Open invitations" className="divide-y border-y border-border">
          {invitations.map((invitation) => (
            <li
              key={invitation.id}
              className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 px-3 py-2"
            >
              <div className="min-w-0 space-y-0.5">
                <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                  <span className="min-w-0 break-words text-[13px] font-medium">
                    {invitee(invitation)}
                  </span>
                  <Chip color="default" variant="tertiary" size="sm">
                    <Chip.Label>
                      {invitation.member_id
                        ? `links ${memberName(invitation.member_id)}`
                        : invitation.role}
                    </Chip.Label>
                  </Chip>
                </div>
                <p className="min-w-0 break-words text-xs text-muted-foreground">
                  invited by {memberName(invitation.created_by)} ·{' '}
                  <span title={invitation.expires_at}>
                    expires {timeAgo(invitation.expires_at)}
                  </span>
                </p>
              </div>
              <Button
                size="default"
                variant="ghost"
                aria-label={`Revoke invitation for ${invitee(invitation)}`}
                onClick={() => setRevoking(invitation)}
              >
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      )}

      {revoking && (
        <RevokeInvitationDialog
          invitation={revoking}
          effect={
            revoking.member_id
              ? `${invitee(revoking)} can no longer connect through the edge as ${memberName(revoking.member_id)}.`
              : `${invitee(revoking)} can no longer join this server by signing in to the edge.`
          }
          client={client}
          onClose={() => setRevoking(null)}
          onRevoked={() => void refetch().catch((err: unknown) => setError(message(err)))}
        />
      )}
    </section>
  )
}

function RevokeInvitationDialog({
  invitation,
  effect,
  client,
  onClose,
  onRevoked,
}: {
  invitation: Invitation
  effect: string
  client: Api
  onClose: () => void
  onRevoked: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const revoke = async () => {
    setBusy(true)
    setError(null)
    try {
      await client.memberInvitationRevoke(invitation.id)
      onRevoked()
      onClose()
      toast.success(`Invitation for ${invitee(invitation)} revoked`)
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
          <AlertDialogTitle>Revoke the invitation for {invitee(invitation)}?</AlertDialogTitle>
          <AlertDialogDescription>{effect}</AlertDialogDescription>
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
              void revoke()
            }}
          >
            Revoke
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
