import { useEffect, useId, useRef, useState } from 'react'
import { toast } from 'sonner'
import { CopyableCommand } from '@/components/copyable-command'
import { Confirm } from '@/components/confirm'
import { Copy } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code } from '@/components/ui/code'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FailureCallout } from '@/components/ui/failure-callout'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { Label } from '@/components/ui/label'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ApiError, codeNotFound, type Api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { errorSentence, message, providerName, timeAgo } from '@/lib/format'
import { useReturnFocus } from '@/lib/hooks'
import { memberName } from '@/lib/member-name'
import type { Invitation, InvitationResult, Member } from '@/lib/types'
import { draftKey, useFailure } from '@/components/failure'
import { roleLabel, roles } from '@/routes/members/roster'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { ListedInvitation } from '@/store/members'
import { optimistic, refusedByServer, stillWaiting, usePastDeadline } from '@/store/optimistic'

export function invitee(invitation: Invitation): string {
  if (invitation.login) return `${invitation.login} on GitHub`
  if (!invitation.provider) return invitation.email ?? ''
  return `${invitation.email} on ${providerName[invitation.provider] ?? invitation.provider}`
}

export interface InviteDraft {
  by: 'login' | 'email'
  who: string
  role: Member['role']
}

/** `member.invitation.create` carries no idempotency key, so an unanswered create is never resent. */
export function createInvitation(client: Api, draft: InviteDraft): Promise<InvitationResult> {
  const { addInvitation, dropInvitation, info } = useStore.getState()
  const same = sameIdentity()
  const target = draft.by === 'login' ? { login: draft.who.trim() } : { email: draft.who.trim() }
  const now = new Date().toISOString()
  const provisional: ListedInvitation = {
    id: crypto.randomUUID(),
    provider: 'github',
    ...target,
    role: draft.role,
    created_by: info?.member.id ?? '',
    created_at: now,
    expires_at: now,
    provisional: true,
  }
  addInvitation(provisional)
  const drop = () => same() && dropInvitation(provisional.id)
  return client.memberInvitationCreate({ ...target, role: draft.role }).then(
    (created) => {
      if (same()) addInvitation(created.invitation, provisional.id)
      return created
    },
    (cause: unknown) => {
      if (refusedByServer(cause)) {
        drop()
        throw cause
      }
      const unsure = new Error(`${errorSentence(cause)} The invitation may have been created; check Open invitations before you invite again.`)
      return client.memberInvitationList().then(
        (list) => {
          const held = list.find((i) => i.login === target.login && i.email === target.email && i.role === draft.role)
          if (!held) {
            drop()
            throw unsure
          }
          if (same()) addInvitation(held, provisional.id)
          throw new Error(`${errorSentence(cause)} The server created the invitation, but its link came only in the answer that was lost; revoke it and invite again.`)
        },
        () => {
          drop()
          throw unsure
        },
      )
    },
  )
}

function Creating() {
  return usePastDeadline(true) ? ` · ${stillWaiting}` : ' · Creating…'
}

export function InvitationsSection({ client }: { client: Api }) {
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const invitations = useStore((s) => s.invitations)
  const [error, setError] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<Invitation | null>(null)
  const { failures, run } = useFailure()

  useEffect(() => {
    let live = true
    const { invitations: since, readInvitations } = useStore.getState()
    readInvitations(client.memberInvitationList()).then(
      (list) => live && useStore.getState().setInvitations(list, since),
      (err: unknown) => live && setError(message(err)),
    )
    return () => {
      live = false
    }
  }, [client])

  const revoke = (invitation: Invitation) => {
    const { addInvitation, dropInvitation, setRevokingInvitation } = useStore.getState()
    const same = sameIdentity()
    let attempts = 0
    void run((answered) => optimistic({
      apply: () => {
        setRevokingInvitation(invitation.id, true)
        dropInvitation(invitation.id)
      },
      send: () => {
        const retried = attempts++ > 0
        return client.memberInvitationRevoke(invitation.id).catch((cause: unknown) => {
          if (!(retried && cause instanceof ApiError && cause.code === codeNotFound)) throw cause
        })
      },
      confirm: () => {
        answered()
        setRevokingInvitation(invitation.id, false)
        if (same()) dropInvitation(invitation.id)
      },
      revert: () => {
        setRevokingInvitation(invitation.id, false)
        if (same()) addInvitation(invitation)
      },
    }), { onDone: () => toast.success(`Invitation for ${invitee(invitation)} revoked`), key: invitation.id })
  }

  const nameOf = (id: string) => memberName({ members, mergedInto }, id)

  return (
    <section aria-label="Invitations" className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h2">Open invitations</SectionLabel>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      {failures.map((failure) => <FailureCallout key={failure.id} error={failure.error} onRetry={failure.retry} />)}
      {invitations?.length === 0 && <p className="text-ui text-muted">No open invitations.</p>}
      {invitations && invitations.length > 0 && (
        <InsetList aria-label="Open invitations">
          {invitations.map((invitation) => (
            <InsetListItem key={invitation.id}>
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-ui font-medium text-text">{invitee(invitation)}</span>
                <span className="truncate text-ui-sm text-muted">
                  {invitation.member_id ? `Links to ${nameOf(invitation.member_id)}` : invitation.role && roleLabel[invitation.role]}
                  {' · invited by '}{nameOf(invitation.created_by)}
                  {invitation.provisional ? <Creating /> : <>{' · expires '}<RelativeTime at={invitation.expires_at} /></>}
                </span>
              </span>
              {!invitation.provisional && (
                <Button size="sm" variant="ghost" aria-label={`Revoke invitation for ${invitee(invitation)}`} onClick={() => setRevoking(invitation)}>
                  Revoke
                </Button>
              )}
            </InsetListItem>
          ))}
        </InsetList>
      )}
      {revoking && (
        <Confirm
          title={`Revoke the invitation for ${invitee(revoking)}?`}
          description={revoking.member_id
            ? `${invitee(revoking)} can no longer connect through the edge as ${nameOf(revoking.member_id)}.`
            : `${invitee(revoking)} can no longer join this server by signing in to the edge.`}
          action="Revoke"
          onConfirm={async () => revoke(revoking)}
          onDone={() => {}}
          onClose={() => setRevoking(null)}
        />
      )}
    </section>
  )
}

export function InviteDialog({ client, draft, error, pending = false, created, onClose, onInvite }: {
  client: Api
  draft?: InviteDraft
  error?: string
  pending?: boolean
  created?: InvitationResult
  onClose: () => void
  onInvite: (draft: InviteDraft) => void
}) {
  const caps = useCapability()
  const byAccount = caps.hasMethod('member.invitation.create')
  const byCode = caps.hasMethod('member.invite')
  const [tab, setTab] = useState(byAccount ? 'account' : 'code')
  const returnFocus = useReturnFocus()
  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-lg" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Invite a member</DialogTitle>
          <DialogDescription>
            {tab === 'account'
              ? 'Invite the GitHub account they sign in with. You get a link to send them; opening it joins this server.'
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
          {byAccount && (
            <TabsContent value="account" className="pt-3">
              {created
                ? <InviteLink created={created} onClose={onClose} />
                : <AccountInvite key={draftKey(draft)} draft={draft} error={error} pending={pending} onClose={onClose} onInvite={onInvite} />}
            </TabsContent>
          )}
          {byCode && <TabsContent value="code" className="pt-3"><CodeInvite client={client} onClose={onClose} /></TabsContent>}
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}

function AccountInvite({ draft, error, pending, onClose, onInvite }: {
  draft?: InviteDraft
  error?: string
  pending: boolean
  onClose: () => void
  onInvite: (draft: InviteDraft) => void
}) {
  const [by, setBy] = useState(draft?.by ?? 'login')
  const [who, setWho] = useState(draft?.who ?? '')
  const [role, setRole] = useState<Member['role']>(draft?.role ?? 'collaborator')
  const late = usePastDeadline(pending)

  return (
    <form
      aria-label="Invite an account"
      className="flex min-w-0 flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault()
        onInvite({ by, who, role })
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
          <p className="text-ui-sm text-muted">
            They open the link and sign in with GitHub as this account. When signing in is enough for this server, that makes them a
            member with this role.
          </p>
          <p className="text-ui-sm text-muted">
            When the server admits approved devices only, their device waits until an admin clicks Approve on its card in Needs you, or
            runs <Code>aether device approve</Code>.
          </p>
          <p className="text-ui-sm text-muted">Invitations expire after 7 days.</p>
        </CollapsibleContent>
      </Collapsible>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      <DialogFooter>
        <Button type="button" variant="secondary" onClick={onClose}>Cancel</Button>
        <Button type="submit" disabled={pending || !who.trim()}>{late ? stillWaiting : pending ? 'Inviting…' : 'Invite'}</Button>
      </DialogFooter>
    </form>
  )
}

function InviteLink({ created, onClose }: { created: InvitationResult; onClose: () => void }) {
  const labelId = useId()
  const codeRef = useRef<HTMLElement>(null)
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex min-w-0 flex-col gap-1">
        <Label id={labelId}>Invite link</Label>
        <div role="group" aria-labelledby={labelId} className="flex min-w-0 items-center gap-1 rounded-control border border-seam bg-canvas px-1.5 py-1">
          <code ref={codeRef} tabIndex={0} title={created.link} className="min-w-0 flex-1 overflow-x-auto whitespace-pre px-1 font-code text-ui-sm leading-5 text-text select-text">
            {created.link}
          </code>
          <Button size="sm" variant="ghost" onClick={() => void copyText(created.link, codeRef.current)}>
            <Copy aria-hidden />
            Copy link
          </Button>
        </div>
      </div>
      <p className="text-ui-sm text-muted">
        Send this link to {invitee(created.invitation)}. It names their GitHub account and expires in 7 days.
      </p>
      <DialogFooter>
        <Button onClick={onClose}>Done</Button>
      </DialogFooter>
    </div>
  )
}

function CodeInvite({ client, onClose }: { client: Api; onClose: () => void }) {
  const [result, setResult] = useState<{ code: string; expires_at: string } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const late = usePastDeadline(busy)

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
        {!result && (
          <Button disabled={busy} onClick={() => void generate()}>
            {late ? stillWaiting : busy ? 'Generating…' : 'Generate code'}
          </Button>
        )}
      </DialogFooter>
    </div>
  )
}
