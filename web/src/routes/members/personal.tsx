import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Input } from '@/components/ui/input'
import { SectionLabel } from '@/components/ui/section-label'
import { api, type Api } from '@/lib/api'
import type { Member } from '@/lib/types'
import { cn } from '@/lib/utils'
import { StopEnvironmentDialog } from '@/routes/environment/stop-environment-dialog'
import { reopen, useFailure, useReopenHost } from '@/components/failure'
import { roleLabel } from '@/routes/members/roster'
import { SignInsSection } from '@/routes/members/sign-ins'
import { GitIdentityForm } from '@/routes/onboarding/git-identity'
import { useStore } from '@/store'
import { nextMemberOrder, type MemberPatch } from '@/store/members'
import { useCapability } from '@/store/hooks'
import { optimistic } from '@/store/optimistic'

// Sent to member.color as data; drawing a member colour inline is the sanctioned exception.
const presetColors = ['#e6194b', '#3cb44b', '#f58231', '#911eb4', '#46f0f0', '#4363d8']

export function ProfileDialog({ open, onOpenChange, client = api }: { open: boolean; onOpenChange: (open: boolean) => void; client?: Api }) {
  const self = useStore((s) => s.info?.member)
  const color = useStore((s) => (s.info ? s.members[s.info.member.id]?.color : undefined)) ?? self?.color
  const caps = useCapability()
  const [reopened, setReopened] = useState<{ draft: NameDraft; n: number }>()
  useReopenHost<NameDraft>('profile', (draft) => {
    setReopened((last) => ({ draft, n: (last?.n ?? 0) + 1 }))
    onOpenChange(true)
  })
  const [signIns, setSignIns] = useState(false)
  useReopenHost('sign-ins', () => {
    setSignIns(true)
    onOpenChange(true)
  })
  if (!self) return null
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          setReopened(undefined)
          setSignIns(false)
        }
        onOpenChange(next)
      }}
    >
      <DialogContent
        className="sm:max-w-xl"
        onOpenAutoFocus={(event) => {
          // The display name field comes first; focusing it would raise a phone's keyboard on open.
          event.preventDefault()
          ;(event.currentTarget as HTMLElement).focus()
        }}
      >
        <DialogHeader>
          <DialogTitle>Profile</DialogTitle>
          <DialogDescription className="flex items-center gap-2">
            <Avatar name={self.display_name} color={color} size="header" />
            <span className="text-text">{self.display_name}</span>
            <span>· {roleLabel[self.role]}</span>
          </DialogDescription>
        </DialogHeader>
        <div className="flex min-w-0 flex-col gap-6">
          {caps.hasMethod('member.rename') && <DisplayNameForm key={reopened?.n} client={client} self={self.id} current={self.display_name} reopened={reopened?.draft} />}
          {caps.hasMethod('member.color') && <ColorPicker client={client} self={self.id} current={color ?? ''} />}
          {caps.hasMethod('member.git') && (
            <section aria-label="Git identity" className="flex min-w-0 flex-col gap-2">
              <SectionLabel as="h3">Git identity</SectionLabel>
              <GitIdentityForm client={client} caps={caps} />
            </section>
          )}
          {caps.hasMethod('account.share') && caps.hasMethod('account.revoke') && <AccountSharing client={client} self={self} />}
          {caps.hasMethod('member.identity.list') && <SignInsSection client={client} self={self} focus={signIns} />}
        </div>
      </DialogContent>
    </Dialog>
  )
}

interface NameDraft {
  name: string
  error: string
}

function DisplayNameForm({ client, self, current, reopened }: { client: Api; self: string; current: string; reopened?: NameDraft }) {
  const { patchMember, revertMember, confirmMember } = useStore.getState()
  const [name, setName] = useState(reopened?.name ?? current)
  const [reopenedError, setReopenedError] = useState(reopened?.error)
  const { failures, run } = useFailure()
  const save = () => {
    const typed = name
    let patch: MemberPatch | undefined
    const order = nextMemberOrder()
    setReopenedError(undefined)
    void run((answered) => optimistic({
      apply: () => {
        patch = patchMember(self, { display_name: typed.trim() }, order)
      },
      send: () => client.memberRename(typed),
      confirm: (member) => {
        answered()
        if (confirmMember(self, { display_name: member.display_name }, patch)) setName((value) => (value === typed ? member.display_name : value))
      },
      revert: () => revertMember(self, patch),
    }), {
      onDone: () => toast.success('Display name saved'),
      key: 'rename',
      reopen: (error) => reopen<NameDraft>('profile', { name: typed, error }),
    })
  }
  return (
    <form
      aria-label="Display name"
      className="flex min-w-0 flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <SectionLabel as="h3">Display name</SectionLabel>
      <p className="text-ui-sm text-muted">Shown to teammates on your avatar, runs and messages.</p>
      <div className="flex min-w-0 items-center gap-2">
        <Input
          aria-label="Display name"
          className="min-w-0 flex-1"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Button type="submit" size="sm" variant="secondary" disabled={!name.trim() || name.trim() === current}>
          Save
        </Button>
      </div>
      {reopenedError && <FailureCallout error={reopenedError} />}
      {failures.map((failure) => <FailureCallout key={failure.id} error={failure.error} onRetry={failure.retry} />)}
    </form>
  )
}

function ColorPicker({ client, self, current }: { client: Api; self: string; current: string }) {
  const { patchMember, revertMember, confirmMember } = useStore.getState()
  const { failures, run } = useFailure()
  const recolor = (color: string) => {
    const order = nextMemberOrder()
    let patch: MemberPatch | undefined
    void run((answered) => optimistic({
      apply: () => {
        patch = patchMember(self, { color }, order)
      },
      send: () => client.memberColor(color),
      confirm: (member) => {
        answered()
        confirmMember(self, { color: member.color }, patch)
      },
      revert: () => revertMember(self, patch),
    }), { key: 'colour' })
  }
  return (
    <section aria-label="Your colour" className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h3">Colour</SectionLabel>
      <p className="text-ui-sm text-muted">Rings your avatar wherever your presence and runs appear.</p>
      <div className="flex flex-wrap items-center gap-1">
        {presetColors.map((color) => (
          <Button key={color} variant="ghost" size="icon" label={`Set colour ${color}`} aria-pressed={current === color} onClick={() => recolor(color)}>
            <span
              className={cn('size-4 rounded-full', current === color && 'ring-2 ring-text ring-offset-2 ring-offset-raised')}
              style={{ backgroundColor: color }}
            />
          </Button>
        ))}
      </div>
      {failures.map((failure) => <FailureCallout key={failure.id} error={failure.error} onRetry={failure.retry} />)}
    </section>
  )
}

function AccountSharing({ client, self }: { client: Api; self: Member }) {
  const members = useStore((s) => s.members)
  const caps = useCapability()
  const [sharedWith, setSharedWith] = useState<Member[]>([])
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set())
  const { failures, run } = useFailure()
  // Per teammate: the newest share or revoke started, so an older late answer leaves a newer one alone.
  const toggles = useRef(new Map<string, number>())
  // Set by a first share, which only containers created after it honour.
  const [firstShare, setFirstShare] = useState<Member | null>(null)
  const [stopping, setStopping] = useState(false)
  const [terminalUnread, setTerminalUnread] = useState(false)
  // Drops a terminal read answered after a stop: it describes the ended terminal.
  const terminalRead = useRef(0)
  const terminalRunning = useStore((s) => s.envTerminal.status?.running === true)
  const setTerminalStatus = useStore((s) => s.setEnvTerminalStatus)
  const teammates = Object.values(members).filter((m) => !m.pending && m.id !== self.id)

  // Teammates shared or revoked since the list read was sent keep what is shown: the read may predate the change.
  const changed = useRef(new Set<string>())
  // Teammates the server holds a grant for, as far as the list read and answers since tell.
  const saved = useRef(new Set<string>())

  useEffect(() => {
    let live = true
    const since = new Set<string>()
    changed.current = since
    client.accountList().then(
      (access) => {
        if (!live) return
        const read = access.shared_with.filter((m) => !since.has(m.id))
        saved.current = new Set([...read.map((m) => m.id), ...[...saved.current].filter((id) => since.has(id))])
        setSharedWith((list) => [...read, ...list.filter((m) => since.has(m.id))])
      },
      () => {},
    )
    return () => {
      live = false
    }
  }, [client])

  const toggle = (member: Member) => {
    const shared = sharedWith.some((entry) => entry.id === member.id)
    const without = (list: Member[]) => list.filter((entry) => entry.id !== member.id)
    const flip = (share: boolean) => setSharedWith((list) => (share ? [...without(list), member] : without(list)))
    const settle = () => setPending((ids) => new Set([...ids].filter((id) => id !== member.id)))
    const attempt = (toggles.current.get(member.id) ?? 0) + 1
    toggles.current.set(member.id, attempt)
    changed.current.add(member.id)
    void run((answered) => optimistic({
      apply: () => {
        flip(!shared)
        setPending((ids) => new Set(ids).add(member.id))
      },
      send: () => (shared ? client.accountRevoke(member.id) : client.accountShare(member.id)),
      confirm: () => {
        answered()
        const first = !shared && saved.current.size === 0
        if (shared) saved.current.delete(member.id)
        else saved.current.add(member.id)
        if (toggles.current.get(member.id) !== attempt) return
        flip(!shared)
        settle()
        if (shared) {
          setFirstShare(null)
          return
        }
        // The share stands from here on, so the advice must not wait on the reads that follow.
        if (first && caps.hasWS('terminal')) {
          setFirstShare(member)
          setTerminalUnread(false)
          const read = ++terminalRead.current
          client.terminalStatus().then(
            (status) => terminalRead.current === read && setTerminalStatus(status),
            () => terminalRead.current === read && setTerminalUnread(true),
          )
        }
      },
      revert: () => {
        if (toggles.current.get(member.id) !== attempt) return
        flip(saved.current.has(member.id))
        settle()
      },
    }), {
      onDone: () => toast.success(shared ? `Account access revoked from ${member.display_name}` : `Account shared with ${member.display_name}`),
      key: member.id,
    })
  }

  return (
    <section aria-label="Account sharing" className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h3">Agent account sharing</SectionLabel>
      <div className="flex min-w-0 flex-col">
        <p className="text-ui-sm text-muted">Teammates you share with can launch runs on your agent logins and quota; their runs stay theirs.</p>
        <Collapsible>
          <CollapsibleTrigger><span className="text-muted">Learn more</span></CollapsibleTrigger>
          <CollapsibleContent className="flex flex-col gap-2 pt-1 pl-4">
            <p className="text-ui-sm text-muted">Their runs can use, refresh, replace or log out your agent logins. Your environment, files and GitHub login are not shared, and running agents are not stopped when you revoke.</p>
            <p className="text-ui-sm text-muted">Except omp: an omp share hands over your whole omp agent directory, where their runs can plant code your own omp sessions run with your home. Share omp only with someone you would give your home to.</p>
          </CollapsibleContent>
        </Collapsible>
      </div>
      {firstShare && (terminalRunning || terminalUnread) && (
        <Callout
          tone="needs-you"
          role="status"
          actions={<Button size="sm" variant="secondary" onClick={() => setStopping(true)}>Stop environment</Button>}
        >
          {terminalRunning
            ? 'Your Environment was started before you shared, so a'
            : 'Your Environment could not be checked. If it is open, it was started before you shared, so a'}{' '}
          Claude Code login written there will not reach {firstShare.display_name}&apos;s runs until you stop it and open it again from
          Environment. Runs you already have running keep the mounts they started with until they end.
        </Callout>
      )}
      {failures.map((failure) => <FailureCallout key={failure.id} error={failure.error} onRetry={failure.retry} />)}
      {teammates.length === 0 ? (
        <p className="text-ui text-muted">No teammates yet.</p>
      ) : (
        <ul className="flex flex-col divide-y divide-seam rounded-panel border border-seam">
          {teammates.map((member) => {
            const shared = sharedWith.some((entry) => entry.id === member.id)
            return (
              <li key={member.id} className="flex min-h-11 min-w-0 items-center gap-3 px-3 py-1.5">
                <Avatar name={member.display_name} color={member.color} size="header" />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-ui font-medium text-text">{member.display_name}</span>
                  <span className="text-ui-sm text-muted">{shared ? 'Can use your account' : 'Not shared'}</span>
                </span>
                <Button size="sm" variant="secondary" disabled={pending.has(member.id)} onClick={() => toggle(member)}>
                  {shared ? 'Revoke access' : 'Share account'}
                </Button>
              </li>
            )
          })}
        </ul>
      )}
      {stopping && (
        <StopEnvironmentDialog
          client={client}
          onClose={() => setStopping(false)}
          onStopped={() => {
            terminalRead.current++
            setFirstShare(null)
          }}
        />
      )}
    </section>
  )
}
