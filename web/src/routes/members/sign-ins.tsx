import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { Copy } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import { Code } from '@/components/ui/code'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Radio, RadioGroup } from '@/components/ui/radio'
import { RelativeTime } from '@/components/ui/relative-time'
import { SectionLabel } from '@/components/ui/section-label'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { Api } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { toastFailure } from '@/components/failure'
import { message, providerName } from '@/lib/format'
import type { InvitationResult, Member, MemberIdentities, MemberMerge, MergeCode, MergeMember, MergePlan, MergeProposal } from '@/lib/types'
import { deviceStatus } from '@/routes/devices'
import { acceptOutcome, proposeOutcome, settingsLines, type MergeOutcome, type ProposeForm } from '@/routes/members/merge-plan'
import { roleLabel } from '@/routes/members/roster'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { memberName } from '@/lib/member-name'
import { dropMergeProposal, loadMergeProposals } from '@/store/members'

type Planned = MergeProposal & { plan: MergePlan }

/** The viewer's open proposals: those to accept, those to accept whose plan
 * the server refused to compute (only Cancel is left), and those the viewer made. */
export function useOpenProposals(): { toAccept: Planned[]; unplanned: MergeProposal[]; outgoing: MergeProposal[] } {
  const self = useStore((s) => s.info?.member.id)
  const proposals = useStore((s) => s.mergeProposals)
  return useMemo(() => ({
    toAccept: proposals.filter((p): p is Planned => p.minted_id === self && p.plan !== undefined),
    unplanned: proposals.filter((p) => p.minted_id === self && p.plan === undefined),
    outgoing: proposals.filter((p) => p.proposer_id === self),
  }), [proposals, self])
}

export function ProposalsReadError() {
  const error = useStore((s) => s.mergeProposalsError)
  return error && <Callout tone="failed" role="alert">Reading the open merge proposals failed: {error}</Callout>
}

/** Errors go to `onFailed`, so one outlives a proposal that a reload removes. */
function OpenProposals({ client, onMerged, onFailed }: { client: Api; onMerged: (merge: MemberMerge) => void; onFailed: (error: string) => void }) {
  const { toAccept, unplanned, outgoing } = useOpenProposals()
  const names = useNames()
  return (
    <>
      {toAccept.map((proposal) => (
        <ProposalToAccept key={proposal.id} client={client} proposal={proposal} onMerged={onMerged} onFailed={onFailed} />
      ))}
      {unplanned.map((proposal) => (
        <Callout key={proposal.id} tone="needs-you" role="region" aria-label={`Merge proposal from ${names.member(proposal.proposer_id)}`}
          title={`${names.member(proposal.proposer_id)} proposes merging into your member`}
          actions={<CancelProposal client={client} proposal={proposal} onFailed={onFailed} />}
        >
          <p role="alert" className="text-state-failed">{proposal.refusal}</p>
        </Callout>
      ))}
      {outgoing.map((proposal) => (
        <Callout key={proposal.id} tone="neutral" role="region" aria-label={`Your merge proposal to ${proposal.plan?.minted.display_name ?? names.member(proposal.minted_id)}`}
          actions={<CancelProposal client={client} proposal={proposal} onFailed={onFailed} />}
        >
          <p>
            Your proposal to merge with {proposal.plan?.minted.display_name ?? names.member(proposal.minted_id)} waits for them to accept it, until{' '}
            <RelativeTime at={proposal.expires_at} />.
          </p>
          {proposal.refusal && <p role="alert" className="text-state-failed">{proposal.refusal}</p>}
        </Callout>
      ))}
    </>
  )
}

function CancelProposal({ client, proposal, onFailed }: { client: Api; proposal: MergeProposal; onFailed: (error: string) => void }) {
  const [pending, setPending] = useState(false)
  const cancel = async () => {
    setPending(true)
    try {
      await client.memberMergeCancel(proposal.id)
      dropMergeProposal(useStore, proposal.id)
    } catch (err) {
      onFailed(message(err))
    } finally {
      setPending(false)
    }
  }
  return <Button size="sm" variant="secondary" disabled={pending} onClick={() => void cancel()}>{pending ? 'Cancelling…' : 'Cancel'}</Button>
}

/** An action's error, shown in place while its view is mounted and as a
 * toast once it has closed, unless the signed-in member changed. */
function useActionError() {
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(true)
  const [same] = useState(sameIdentity)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const report = useCallback((next: string | null) => {
    if (mounted.current) setError(next)
    else if (next && same()) toastFailure(next, { same })
  }, [same])
  return [error, report] as const
}

function useNames() {
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const workspaces = useStore((s) => s.workspaces)
  return {
    member: (id: string) => memberName({ members, mergedInto }, id),
    workspace: (id: string) => workspaces[id]?.name ?? id,
  }
}

export function SignInsSection({ client, self, focus }: { client: Api; self: Member; focus?: boolean }) {
  const caps = useCapability()
  const merges = useStore((s) => Object.keys(s.mergedInto).length)
  // Re-read whenever member.device or a wake re-reads the devices: an approval binds an account.
  const devices = useStore((s) => s.devices)
  const [identities, setIdentities] = useState<MemberIdentities | null>(null)
  const [error, setError] = useActionError()
  const [linking, setLinking] = useState(false)
  const [merging, setMerging] = useState(false)
  const [merged, setMerged] = useState<MemberMerge | null>(null)
  const ref = useRef<HTMLElement>(null)

  const reads = useRef(0)
  const reload = useCallback(() => {
    const read = ++reads.current
    setError(null)
    return client.memberIdentityList(self.id).then(
      (list) => read === reads.current && setIdentities(list),
      (err: unknown) => read === reads.current && setError(message(err)),
    )
  }, [client, self.id, setError])

  useEffect(() => {
    void reload()
    void loadMergeProposals(useStore, client)
  }, [client, reload, merges, devices])

  useEffect(() => {
    if (focus) ref.current?.scrollIntoView({ block: 'start' })
  }, [focus])

  const onMerged = (merge: MemberMerge) => {
    setMerged(merge)
    void reload()
  }

  return (
    <section ref={ref} aria-label="Sign-ins" className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h3">Sign-ins</SectionLabel>
      <p className="text-ui-sm text-muted">Every way you reach this server as this member.</p>
      <ProposalsReadError />
      <OpenProposals client={client} onMerged={onMerged} onFailed={setError} />
      {merged && <MergeRecord merge={merged} />}
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      {identities && <SignInList identities={identities} />}
      <div className="flex flex-wrap gap-2">
        {caps.hasMethod('member.identity.link') && <Button size="sm" variant="secondary" onClick={() => setLinking(true)}>Link a GitHub account</Button>}
        {caps.hasMethod('member.merge.start') && <Button size="sm" variant="secondary" onClick={() => setMerging(true)}>Merge with another member</Button>}
      </div>
      {caps.hasMethod('member.merge.list') && <MergeHistory client={client} />}
      {linking && <LinkDialog client={client} self={self} onClose={() => setLinking(false)} />}
      {merging && <MergeDialog client={client} self={self} onMerged={onMerged} onClose={() => setMerging(false)} />}
    </section>
  )
}

function SignInList({ identities }: { identities: MemberIdentities }) {
  const rows = [
    identities.tailnet_login && { key: 'tailnet', label: 'Tailnet login', value: identities.tailnet_login },
    identities.public_key_fingerprint && { key: 'ssh', label: 'SSH key', value: identities.public_key_fingerprint },
    ...identities.identities.map((id) => ({
      key: `${id.provider}:${id.subject}`,
      label: `${providerName[id.provider] ?? id.provider} account`,
      value: id.login ?? id.email ?? id.subject,
    })),
  ].filter((row): row is { key: string; label: string; value: string } => Boolean(row))
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <ul aria-label="Your sign-ins" className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
        {rows.map((row) => (
          <li key={row.key} className="flex min-h-11 min-w-0 flex-col justify-center px-3 py-2">
            <span className="text-ui-sm text-muted">{row.label}</span>
            <span className="break-all font-code text-ui-sm text-text">{row.value}</span>
          </li>
        ))}
      </ul>
      {identities.devices.length > 0 && (
        <ul aria-label="Your devices" className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
          {identities.devices.map((device) => (
            <li key={device.id} className="flex min-h-11 min-w-0 flex-col justify-center px-3 py-2">
              <span className="flex min-w-0 items-center gap-2">
                <span className="truncate text-ui font-medium text-text">{device.label}</span>
                <Badge tone={deviceStatus[device.status].tone}>{deviceStatus[device.status].label}</Badge>
              </span>
              <span className="truncate text-ui-sm text-muted">
                {device.account} on {providerName[device.provider] ?? device.provider}
                {' · '}{device.last_seen_at ? <>last seen <RelativeTime at={device.last_seen_at} /></> : 'never seen'}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function CopyField({ label, value }: { label: string; value: string }) {
  const labelId = useId()
  const codeRef = useRef<HTMLElement>(null)
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <Label id={labelId}>{label}</Label>
      <div role="group" aria-labelledby={labelId} className="flex min-w-0 items-center gap-1 rounded-control border border-seam bg-canvas px-1.5 py-1">
        <code ref={codeRef} tabIndex={0} title={value} className="min-w-0 flex-1 overflow-x-auto whitespace-pre px-1 font-code text-ui-sm leading-5 text-text select-text">
          {value}
        </code>
        <Button size="sm" variant="ghost" onClick={() => void copyText(value, codeRef.current)}>
          <Copy aria-hidden />
          Copy
        </Button>
      </div>
    </div>
  )
}

function LinkDialog({ client, self, onClose }: { client: Api; self: Member; onClose: () => void }) {
  const [login, setLogin] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useActionError()
  const [created, setCreated] = useState<InvitationResult | null>(null)

  const link = async () => {
    setPending(true)
    setError(null)
    try {
      setCreated(await client.memberIdentityLink({ login: login.trim() }))
    } catch (err) {
      setError(message(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Link a GitHub account</DialogTitle>
          <DialogDescription>Sign in through the server&apos;s edge with that account as {self.display_name}.</DialogDescription>
        </DialogHeader>
        {created ? (
          <div className="flex min-w-0 flex-col gap-3">
            <CopyField label="Join link" value={created.link} />
            <p className="text-ui text-text">
              Open this link on a device signed in to GitHub as {created.invitation.login ?? created.invitation.email}, or run{' '}
              <Code>aether link &apos;{created.link}&apos;</Code> there.
            </p>
            <p className="text-ui-sm text-muted">
              That device then waits until you approve it from here: <strong>Approve</strong> on its Needs you card, or{' '}
              <Code>aether device approve &lt;device id&gt;</Code>. Approving binds the account to {self.display_name}. A new link replaces this one;
              the link expires <RelativeTime at={created.invitation.expires_at} />.
            </p>
            <DialogFooter>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            aria-label="Link a GitHub account"
            className="flex min-w-0 flex-col gap-3"
            onSubmit={(e) => {
              e.preventDefault()
              void link()
            }}
          >
            <FormField label="GitHub login">
              <Input value={login} autoComplete="off" placeholder="octocat" disabled={pending} onChange={(e) => setLogin(e.target.value)} />
            </FormField>
            {error && <Callout tone="failed" role="alert">{error}</Callout>}
            <DialogFooter>
              <Button type="button" variant="secondary" onClick={onClose}>Cancel</Button>
              <Button type="submit" disabled={pending || !login.trim()}>{pending ? 'Creating link…' : 'Create link'}</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

function useCountdown(until: string | undefined): string {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!until) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [until])
  if (!until) return ''
  const left = Math.max(0, Math.floor((Date.parse(until) - now) / 1000))
  return left === 0 ? 'expired' : `expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`
}

function MergeDialog({ client, self, onMerged, onClose }: { client: Api; self: Member; onMerged: (merge: MemberMerge) => void; onClose: () => void }) {
  const [tab, setTab] = useState('code')
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Merge with another member</DialogTitle>
          <DialogDescription>
            Join two members that are both you, such as a tailnet sign-in and a GitHub sign-in. One side gets a code; the other enters it and
            proposes the merge; the side that got the code accepts it. Nothing undoes a merge.
          </DialogDescription>
        </DialogHeader>
        <Tabs value={tab} onValueChange={setTab}>
          <div className="border-b border-seam">
            <TabsList aria-label="Merge step">
              <TabsTrigger value="code">Get a merge code</TabsTrigger>
              <TabsTrigger value="enter">I have a merge code</TabsTrigger>
            </TabsList>
          </div>
          <TabsContent value="code" className="pt-3">
            <GetCode client={client} self={self} onMerged={onMerged} />
          </TabsContent>
          <TabsContent value="enter" className="pt-3">
            <EnterCode client={client} self={self} />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}

function GetCode({ client, self, onMerged }: { client: Api; self: Member; onMerged: (merge: MemberMerge) => void }) {
  const [code, setCode] = useState<MergeCode | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useActionError()
  const countdown = useCountdown(code?.expires_at)

  const start = async () => {
    setPending(true)
    setError(null)
    try {
      const minted = await client.memberMergeStart()
      setCode(minted)
      useStore.getState().setMergeCodeUntil(Date.parse(minted.expires_at))
    } catch (err) {
      setError(message(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      {code ? (
        <>
          <CopyField label="Merge code" value={code.code} />
          <p role="timer" aria-live="off" className="text-ui-sm tabular-nums text-muted">This code {countdown}. It works once.</p>
          <p className="text-ui text-text">
            On your other sign-in, open Profile › Sign-ins › Merge with another member › I have a merge code, or run{' '}
            <Code>aether member merge {code.code}</Code>.
          </p>
          <p className="text-ui-sm text-muted">
            Anyone who enters this code can propose joining their sign-ins to {self.display_name}; you will be asked to accept here.
          </p>
        </>
      ) : (
        <p className="text-ui text-muted">The code is valid for 10 minutes and shown once. A new code replaces your open one.</p>
      )}
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      <ProposalsReadError />
      <OpenProposals client={client} onMerged={onMerged} onFailed={setError} />
      <div className="flex justify-end">
        <Button variant={code ? 'secondary' : 'primary'} disabled={pending} onClick={() => void start()}>
          {pending ? 'Getting a code…' : code ? 'Get a new code' : 'Get a merge code'}
        </Button>
      </div>
    </div>
  )
}

function EnterCode({ client, self }: { client: Api; self: Member }) {
  const [code, setCode] = useState('')
  const [plan, setPlan] = useState<{ code: string; plan: MergePlan } | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useActionError()
  // Kept with the member it was proposed to: the code may have changed while the propose was answered.
  const [proposal, setProposal] = useState<{ proposal: MergeProposal; minted: MergeMember } | null>(null)

  const preview = async () => {
    setPending(true)
    setError(null)
    setPlan(null)
    try {
      const typed = code.trim()
      setPlan({ code: typed, plan: await client.memberMergePreview(typed) })
    } catch (err) {
      setError(message(err))
    } finally {
      setPending(false)
    }
  }

  if (proposal) {
    return (
      <div className="flex min-w-0 flex-col gap-3">
        <Proposed proposal={proposal.proposal} minted={proposal.minted} />
        <ProposalsReadError />
      </div>
    )
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <form
        aria-label="Enter a merge code"
        className="flex min-w-0 flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          void preview()
        }}
      >
        <FormField label="Merge code" className="min-w-0 flex-[1_1_12rem]">
          <Input
            className="font-code"
            value={code}
            placeholder="ABCD-EFGH-IJKL-MNOP"
            autoComplete="off"
            disabled={pending}
            onChange={(e) => {
              setCode(e.target.value)
              setPlan(null)
            }}
          />
        </FormField>
        <Button type="submit" variant="secondary" disabled={pending || !code.trim()}>{pending ? 'Previewing…' : 'Preview'}</Button>
      </form>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      {plan && (
        <ProposeForm
          key={plan.plan.plan_digest}
          client={client}
          self={self}
          code={plan.code}
          plan={plan.plan}
          onProposed={(proposed) => {
            setProposal({ proposal: proposed, minted: plan.plan.minted })
            void loadMergeProposals(useStore, client)
          }}
          onRefused={(refusal) => {
            setPlan(null)
            setError(refusal)
          }}
        />
      )}
    </div>
  )
}

function Proposed({ proposal, minted }: { proposal: MergeProposal; minted: MergeMember }) {
  return (
    <Callout tone="done" role="status" title="Merge proposed">
      {minted.display_name} must accept it on the other sign-in: Profile › Sign-ins, or <Code>aether member merge accept</Code>. Until then nothing
      changes. The proposal is open until <RelativeTime at={proposal.expires_at} />; <Code>aether member merge cancel</Code> drops it.
    </Callout>
  )
}

/** A refused propose ends this form: the plan it previewed may no longer hold, so the caller previews again. */
export function ProposeForm({ client, self, code, plan, onProposed, onRefused }: {
  client: Api
  self: Member
  code: string
  plan: MergePlan
  onProposed: (proposal: MergeProposal) => void
  onRefused: (error: string) => void
}) {
  const [form, setForm] = useState<ProposeForm>({ keep: null, discardAck: false, keepAdmin: false })
  const [pending, setPending] = useState(false)
  const shown = proposeOutcome(plan, form)
  const keepId = useId()

  const propose = async () => {
    if (!form.keep) return
    setPending(true)
    let proposed: MergeProposal
    try {
      proposed = await client.memberMergePropose({
        code,
        keep_settings_of: form.keep,
        plan_digest: plan.plan_digest,
        discard_ack: true,
        ...(shown.keepAdminOffered && form.keepAdmin && { keep_admin: true }),
      })
    } catch (err) {
      setPending(false)
      return onRefused(message(err))
    }
    setPending(false)
    onProposed(proposed)
  }

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <Sides plan={plan} self={plan.proposer} other={plan.minted} />
      <div className="flex min-w-0 flex-col gap-2">
        <Label id={keepId}>Keep settings from</Label>
        <RadioGroup aria-labelledby={keepId} aria-required value={form.keep ?? ''} onValueChange={(keep) => setForm({ ...form, keep, discardAck: false })}>
          {[plan.proposer, plan.minted].map((side) => (
            <label key={side.id} className="flex min-h-11 min-w-0 cursor-pointer items-center gap-2 text-ui text-text coarse:min-h-11">
              <Radio value={side.id} />
              <span className="min-w-0 break-words">{side.display_name} ({side.id === self.id ? 'this sign-in' : 'the other sign-in'})</span>
            </label>
          ))}
        </RadioGroup>
      </div>
      <Consequences plan={plan} shown={shown} />
      {shown.keepAdminOffered && (
        <CheckRow checked={form.keepAdmin} onChange={(keepAdmin) => setForm({ ...form, keepAdmin })}>
          Keep the merged member an admin
        </CheckRow>
      )}
      {shown.choice && (
        <CheckRow checked={form.discardAck} onChange={(discardAck) => setForm({ ...form, discardAck })}>
          Discard the listed settings
        </CheckRow>
      )}
      <div className="flex justify-end">
        <Button variant="danger" disabled={pending || !shown.canSubmit} onClick={() => void propose()}>
          {pending ? 'Proposing…' : 'Propose merge'}
        </Button>
      </div>
    </div>
  )
}

function CheckRow({ checked, onChange, children }: { checked: boolean; onChange: (checked: boolean) => void; children: ReactNode }) {
  const id = useId()
  return (
    <div className="flex min-h-11 min-w-0 items-center gap-2">
      <Checkbox id={id} checked={checked} onCheckedChange={(next) => onChange(next === true)} />
      <Label htmlFor={id} className="text-ui text-text">{children}</Label>
    </div>
  )
}

function Consequences({ plan, shown }: { plan: MergePlan; shown: MergeOutcome }) {
  const names = useNames()
  const rolesDiffer = plan.minted.role !== plan.proposer.role
  return (
    <div className="flex min-w-0 flex-col gap-2">
      {rolesDiffer && (
        <p className="text-ui text-text">
          The roles differ ({plan.proposer.display_name}: {roleLabel[plan.proposer.role]}, {plan.minted.display_name}: {roleLabel[plan.minted.role]}).
          The merged member will be <strong>{roleLabel[shown.role]}</strong>
          {plan.role_with_keep_admin && !shown.keepAdminOffered && shown.role !== plan.role_with_keep_admin
            ? `, or ${roleLabel[plan.role_with_keep_admin]} if the admin side keeps admin when it accepts`
            : ''}
          .
        </p>
      )}
      {plan.keep_admin_required && (
        <p className="text-ui text-text">This merge is refused unless the admin side keeps admin: the server would have no admin left.</p>
      )}
      {shown.choice && shown.survivor && shown.absorbed && (
        <div className="flex min-w-0 flex-col gap-1">
          <p className="text-ui text-text">
            {shown.survivor.display_name} survives with its id ({shown.survivor.id}). {shown.absorbed.display_name}&apos;s sign-ins, devices, runs and
            schedules move to it. These settings of {shown.absorbed.display_name} are discarded:
          </p>
          <ul aria-label="Discarded settings" className="list-disc pl-5 text-ui text-text">
            {settingsLines(shown.choice.discarded, names).map((line) => <li key={line} className="break-words">{line}</li>)}
          </ul>
        </div>
      )}
    </div>
  )
}

function Sides({ plan, self, other }: { plan: MergePlan; self: MergeMember; other: MergeMember }) {
  return (
    <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
      <Side title="This sign-in" member={self} />
      <Side title="The other sign-in" member={other} />
      <p className="text-ui-sm text-muted sm:col-span-2">After the merge, the member signs in with: {plan.sign_ins.join('; ')}.</p>
    </div>
  )
}

function Side({ title, member }: { title: string; member: MergeMember }) {
  const names = useNames()
  const settings = member.settings
  const rows: [string, ReactNode][] = [
    ['Role', roleLabel[member.role]],
    ...(member.tailnet_login ? [['Tailnet login', member.tailnet_login] as [string, string]] : []),
    ...(member.public_key_fingerprint ? [['SSH key', member.public_key_fingerprint] as [string, string]] : []),
    ['GitHub', member.identities.length ? member.identities.map((i) => i.login).join(', ') : 'none'],
    ['Devices', member.devices.length ? member.devices.map((d) => `${d.label} (${deviceStatus[d.status].label.toLowerCase()})`).join(', ') : 'none'],
    ['Runs', String(member.runs)],
    ['Schedules', String(member.schedules)],
    ['Settings', settingsLines(settings, names).join('; ')],
  ]
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-1 rounded-panel border border-seam p-3">
      <SectionLabel as="h3">{title}</SectionLabel>
      <p className="break-words text-ui font-medium text-text">{member.display_name} <span className="font-code text-ui-sm text-muted">{member.id}</span></p>
      <dl className="grid min-w-0 grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm">
        {rows.map(([label, value]) => (
          <div key={label} className="contents">
            <dt className="text-muted">{label}</dt>
            <dd className="break-words text-text">{value}</dd>
          </div>
        ))}
      </dl>
    </section>
  )
}

function ProposalToAccept({ client, proposal, onMerged, onFailed }: {
  client: Api
  proposal: Planned
  onMerged: (merge: MemberMerge) => void
  onFailed: (error: string) => void
}) {
  const [discardAck, setDiscardAck] = useState(false)
  const [keepAdmin, setKeepAdmin] = useState(false)
  const [pending, setPending] = useState<'accept' | 'cancel' | null>(null)
  const { plan } = proposal
  const shown = acceptOutcome(proposal, discardAck, keepAdmin)
  const names = useNames()

  const act = async (verb: 'accept' | 'cancel') => {
    setPending(verb)
    try {
      if (verb === 'accept') {
        const result = await client.memberMergeAccept({
          proposal_id: proposal.id,
          plan_digest: proposal.plan_digest,
          ...(shown.keepAdminOffered && keepAdmin && { keep_admin: true }),
        })
        onMerged(result.merge)
      } else {
        await client.memberMergeCancel(proposal.id)
      }
      // After an accept the server closes this connection; the next snapshot re-reads the rest.
      dropMergeProposal(useStore, proposal.id)
    } catch (err) {
      onFailed(message(err))
      setDiscardAck(false)
      // A refused accept usually means the plan changed; the re-read shows why and disables Accept.
      await loadMergeProposals(useStore, client)
    } finally {
      setPending(null)
    }
  }

  return (
    <Callout tone="needs-you" role="region" aria-label={`Merge proposal from ${plan.proposer.display_name}`} title={`${plan.proposer.display_name} proposes merging into your member`}>
      <div className="flex min-w-0 flex-col gap-2 pt-1">
        <p>
          Their sign-ins: {[
            plan.proposer.tailnet_login && `tailnet login ${plan.proposer.tailnet_login}`,
            plan.proposer.public_key_fingerprint && `SSH key ${plan.proposer.public_key_fingerprint}`,
            ...plan.proposer.identities.map((i) => `${providerName[i.provider] ?? i.provider} account ${i.login}`),
          ].filter(Boolean).join('; ') || 'none'}.
        </p>
        {shown.survivor && shown.absorbed && shown.choice && (
          <>
            <p>
              They chose to keep the settings of {shown.survivor.display_name} ({shown.survivor.id === proposal.minted_id ? 'this sign-in' : 'their sign-in'}).
              These settings of {shown.absorbed.display_name} are discarded:
            </p>
            <ul aria-label="Discarded settings" className="list-disc pl-5">
              {settingsLines(shown.choice.discarded, names).map((line) => <li key={line} className="break-words">{line}</li>)}
            </ul>
          </>
        )}
        <p>The merged member will be <strong>{roleLabel[shown.role]}</strong>.</p>
        {proposal.refusal && <p role="alert" className="text-state-failed">{proposal.refusal}</p>}
        {shown.keepAdminOffered && (
          <CheckRow checked={keepAdmin} onChange={setKeepAdmin}>Keep the merged member an admin</CheckRow>
        )}
        <CheckRow checked={discardAck} onChange={setDiscardAck}>Discard the listed settings</CheckRow>
        <div className="flex flex-wrap justify-end gap-2">
          <Button size="sm" variant="secondary" disabled={pending !== null} onClick={() => void act('cancel')}>{pending === 'cancel' ? 'Cancelling…' : 'Cancel'}</Button>
          <Button size="sm" variant="danger" disabled={pending !== null || !shown.canSubmit} onClick={() => void act('accept')}>
            {pending === 'accept' ? 'Merging…' : 'Accept and merge'}
          </Button>
        </div>
      </div>
    </Callout>
  )
}

function MergeRecord({ merge }: { merge: MemberMerge }) {
  const names = useNames()
  return (
    <Callout tone="done" role="status" title="Merged">
      <MergeSummary merge={merge} names={names} />
    </Callout>
  )
}

function MergeSummary({ merge, names }: { merge: MemberMerge; names: ReturnType<typeof useNames> }) {
  const discarded = settingsLines(merge.discarded, names)
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <p className="break-words">
        {merge.absorbed.display_name} ({merge.absorbed_id}) was merged into {names.member(merge.survivor_id)} ({merge.survivor_id}),{' '}
        <RelativeTime at={merge.created_at} />. Role: {roleLabel[merge.survivor_role_before]} and {roleLabel[merge.absorbed_role_before]}, now{' '}
        {roleLabel[merge.role_after]}. Accepted from {merge.confirmed_by_kind} {merge.confirmed_by}.
      </p>
      {discarded.length > 0 && (
        <ul aria-label="Discarded settings" className="list-disc pl-5">
          {discarded.map((line) => <li key={line} className="break-words">{line}</li>)}
          {merge.discarded.harness_definitions.filter((h) => h.definition).map((h) => (
            <li key={`agent:${h.name}`} className="break-words">Custom agent {h.name}: <Code>{h.definition}</Code></li>
          ))}
        </ul>
      )}
      {merge.archive_path && <p className="break-all">Home archived at <Code>{merge.archive_path}</Code>.</p>}
    </div>
  )
}

function MergeHistory({ client }: { client: Api }) {
  const names = useNames()
  const merged = useStore((s) => Object.keys(s.mergedInto).length)
  const [merges, setMerges] = useState<MemberMerge[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    client.memberMergeList().then(
      (list) => live && setMerges(list),
      (err: unknown) => live && setError(message(err)),
    )
    return () => {
      live = false
    }
  }, [client, merged])

  return (
    <section aria-label="Merge history" className="flex min-w-0 flex-col gap-2 pt-2">
      <SectionLabel as="h3">Merge history</SectionLabel>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
      {merges?.length === 0 && <p className="text-ui-sm text-muted">No merges.</p>}
      {merges && merges.length > 0 && (
        <ul aria-label="Merges" className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
          {merges.map((merge) => (
            <li key={merge.id} className="min-w-0 px-3 py-2 text-ui-sm text-text">
              <MergeSummary merge={merge} names={names} />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
