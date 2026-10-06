import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { CopyableCommand } from '@/components/copyable-command'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { Api } from '@/lib/api'
import { message, providerName, timeAgo } from '@/lib/format'
import type { Invitation, Member } from '@/lib/types'
import { roleLabel, roles } from '@/routes/members/roster'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

function invitee(invitation: Invitation): string {
  if (invitation.login) return `${invitation.login} on GitHub`
  if (!invitation.provider) return invitation.email ?? ''
  return `${invitation.email} on ${providerName[invitation.provider] ?? invitation.provider}`
}

export function InvitationsSection({ client, version }: { client: Api; version: number }) {
  const members = useStore((s) => s.members)
  const [invitations, setInvitations] = useState<Invitation[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<Invitation | null>(null)

  const refetch = () => client.memberInvitationList().then(setInvitations, (err: unknown) => setError(message(err)))

  useEffect(() => {
    let live = true
    client.memberInvitationList().then(
      (list) => live && setInvitations(list),
      (err: unknown) => live && setError(message(err)),
    )
    return () => {
      live = false
    }
  }, [client, version])

  const memberName = (id: string) => members[id]?.display_name ?? id

  return (
    <section aria-label="Invitations" className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h2">Open invitations</SectionLabel>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      {invitations?.length === 0 && <p className="text-ui text-muted">No open invitations.</p>}
      {invitations && invitations.length > 0 && (
        <ul aria-label="Open invitations" className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
          {invitations.map((invitation) => (
            <li key={invitation.id} className="flex min-h-11 min-w-0 items-center gap-3 px-3 py-2">
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-ui font-medium text-text">{invitee(invitation)}</span>
                <span className="truncate text-ui-sm text-muted">
                  {invitation.member_id ? `Links to ${memberName(invitation.member_id)}` : invitation.role && roleLabel[invitation.role]}
                  {' · invited by '}{memberName(invitation.created_by)}
                  {' · expires '}<RelativeTime at={invitation.expires_at} />
                </span>
              </span>
              <Button size="sm" variant="ghost" aria-label={`Revoke invitation for ${invitee(invitation)}`} onClick={() => setRevoking(invitation)}>
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      )}
      {revoking && (
        <Confirm
          title={`Revoke the invitation for ${invitee(revoking)}?`}
          description={revoking.member_id
            ? `${invitee(revoking)} can no longer connect through the edge as ${memberName(revoking.member_id)}.`
            : `${invitee(revoking)} can no longer join this server by signing in to the edge.`}
          action="Revoke"
          onConfirm={() => client.memberInvitationRevoke(revoking.id)}
          onDone={() => {
            toast.success(`Invitation for ${invitee(revoking)} revoked`)
            void refetch()
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </section>
  )
}

export function InviteDialog({ client, onClose, onInvited }: { client: Api; onClose: () => void; onInvited: () => void }) {
  const caps = useCapability()
  const byAccount = caps.hasMethod('member.invitation.create')
  const byCode = caps.hasMethod('member.invite')
  const [tab, setTab] = useState(byAccount ? 'account' : 'code')
  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Invite a member</DialogTitle>
          <DialogDescription>
            {tab === 'account'
              ? 'Invite the account they sign in to the edge with; they see this server after aether login.'
              : 'A one-time code joins a computer with aether link. It is shown only here.'}
          </DialogDescription>
        </DialogHeader>
        <Tabs value={tab} onValueChange={setTab}>
          {byAccount && byCode && (
            <TabsList look="segmented" aria-label="Invite with">
              <TabsTrigger value="account">Account</TabsTrigger>
              <TabsTrigger value="code">Invite code</TabsTrigger>
            </TabsList>
          )}
          {byAccount && <TabsContent value="account" className="pt-3"><AccountInvite client={client} onClose={onClose} onInvited={onInvited} /></TabsContent>}
          {byCode && <TabsContent value="code" className="pt-3"><CodeInvite client={client} onClose={onClose} /></TabsContent>}
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}

function AccountInvite({ client, onClose, onInvited }: { client: Api; onClose: () => void; onInvited: () => void }) {
  const [by, setBy] = useState<'login' | 'email'>('login')
  const [who, setWho] = useState('')
  const [role, setRole] = useState<Member['role']>('collaborator')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const invite = async () => {
    setBusy(true)
    setError(null)
    try {
      const invitation = await client.memberInvitationCreate(by === 'login' ? { login: who.trim(), role } : { email: who.trim(), role })
      toast.success(`Invited ${invitee(invitation)}`)
      onInvited()
      onClose()
    } catch (err) {
      setError(message(err))
      setBusy(false)
    }
  }

  return (
    <form
      aria-label="Invite an account"
      className="flex min-w-0 flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault()
        void invite()
      }}
    >
      <div className="grid min-w-0 gap-3 sm:grid-cols-[9rem_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-1">
          <Label htmlFor="invite-by">Invite by</Label>
          <Select value={by} onValueChange={(value) => setBy(value as 'login' | 'email')}>
            <SelectTrigger id="invite-by"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="login">GitHub login</SelectItem>
              <SelectItem value="email">Email</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <FormField label={by === 'login' ? 'GitHub login' : 'Email'}>
          <Input
            type={by === 'email' ? 'email' : 'text'}
            autoComplete="off"
            placeholder={by === 'login' ? 'octocat' : 'dev@example.com'}
            value={who}
            disabled={busy}
            onChange={(e) => setWho(e.target.value)}
          />
        </FormField>
      </div>
      <div className="flex min-w-0 flex-col gap-1">
        <Label htmlFor="invite-role">Role</Label>
        <Select value={role} onValueChange={(value) => setRole(value as Member['role'])}>
          <SelectTrigger id="invite-role" className="w-44"><SelectValue /></SelectTrigger>
          <SelectContent>
            {roles.map((r) => <SelectItem key={r} value={r}>{roleLabel[r]}</SelectItem>)}
          </SelectContent>
        </Select>
      </div>
      <Collapsible>
        <CollapsibleTrigger><span className="text-muted">Learn more</span></CollapsibleTrigger>
        <CollapsibleContent className="flex flex-col gap-2 pt-1 pl-4">
          <p className="text-ui-sm text-muted">When signing in is enough for this server, their first connection makes them a member with this role.</p>
          <p className="text-ui-sm text-muted">
            When the server admits approved devices only, their device waits until an admin approves it with the code it shows, under
            Members &gt; Devices, with <Code>aether device approve</Code>, or with <Code>sudo aether-server device approve</Code> on the server.
          </p>
          <p className="text-ui-sm text-muted">Invitations expire after 7 days.</p>
        </CollapsibleContent>
      </Collapsible>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      <DialogFooter>
        <Button type="button" variant="secondary" onClick={onClose}>Cancel</Button>
        <Button type="submit" disabled={busy || !who.trim()}>Invite</Button>
      </DialogFooter>
    </form>
  )
}

function CodeInvite({ client, onClose }: { client: Api; onClose: () => void }) {
  const [result, setResult] = useState<{ code: string; expires_at: string } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

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

  return (
    <div className="flex min-w-0 flex-col gap-3">
      {result ? (
        <>
          <div aria-label="Invite code"><CopyableCommand command={result.code} /></div>
          <p className="text-ui-sm text-muted">Hand it to the new member. It works once and expires {timeAgo(result.expires_at)}.</p>
        </>
      ) : (
        <p className="text-ui text-muted">Generate a code and hand it to the new member yourself.</p>
      )}
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      <DialogFooter>
        <Button variant="secondary" onClick={onClose}>{result ? 'Done' : 'Cancel'}</Button>
        {!result && <Button disabled={busy} onClick={() => void generate()}>Generate code</Button>}
      </DialogFooter>
    </div>
  )
}
