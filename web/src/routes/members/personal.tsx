import { useState } from 'react'
import { toast } from 'sonner'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FailureCallout } from '@/components/ui/failure-callout'
import { Input } from '@/components/ui/input'
import { SectionLabel } from '@/components/ui/section-label'
import { api, type Api } from '@/lib/api'
import { cn } from '@/lib/utils'
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
  const identity = useStore((s) => s.identityKey)
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
          {caps.hasMethod('member.identity.list') && <SignInsSection key={identity} client={client} self={self} focus={signIns} />}
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
      <p className="text-ui-sm text-muted">Shown to teammates on your avatar, sessions and messages.</p>
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
      <p className="text-ui-sm text-muted">Rings your avatar wherever your presence and sessions appear.</p>
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
