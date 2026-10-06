import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { SectionLabel } from '@/components/ui/section-label'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Member } from '@/lib/types'
import { cn } from '@/lib/utils'
import { StopEnvironmentDialog } from '@/routes/environment/stop-environment-dialog'
import { roleLabel } from '@/routes/members/roster'
import { GitIdentityForm } from '@/routes/onboarding/git-identity'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

// Sent to member.color as data; drawing a member colour inline is the sanctioned exception.
const presetColors = ['#e6194b', '#3cb44b', '#f58231', '#911eb4', '#46f0f0', '#4363d8']

export function ProfileDialog({ open, onOpenChange, client = api }: { open: boolean; onOpenChange: (open: boolean) => void; client?: Api }) {
  const self = useStore((s) => s.info?.member)
  const color = useStore((s) => (s.info ? s.members[s.info.member.id]?.color : undefined)) ?? self?.color
  const caps = useCapability()
  if (!self) return null
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Profile</DialogTitle>
          <DialogDescription className="flex items-center gap-2">
            <Avatar name={self.display_name} color={color} size="header" />
            <span className="text-text">{self.display_name}</span>
            <span>· {roleLabel[self.role]}</span>
          </DialogDescription>
        </DialogHeader>
        <div className="flex min-w-0 flex-col gap-6">
          {caps.hasMethod('member.color') && <ColorPicker client={client} current={color ?? ''} />}
          {caps.hasMethod('member.git') && (
            <section aria-label="Git identity" className="flex min-w-0 flex-col gap-2">
              <SectionLabel as="h3">Git identity</SectionLabel>
              <GitIdentityForm client={client} caps={caps} />
            </section>
          )}
          {caps.hasMethod('account.share') && caps.hasMethod('account.revoke') && <AccountSharing client={client} self={self} />}
        </div>
      </DialogContent>
    </Dialog>
  )
}

function ColorPicker({ client, current }: { client: Api; current: string }) {
  const setMembers = useStore((s) => s.setMembers)
  const [error, setError] = useState<string | null>(null)
  const recolor = async (color: string) => {
    setError(null)
    try {
      await client.memberColor(color)
      setMembers(await client.memberList())
    } catch (err) {
      setError(message(err))
    }
  }
  return (
    <section aria-label="Your colour" className="flex min-w-0 flex-col gap-2">
      <SectionLabel as="h3">Colour</SectionLabel>
      <p className="text-ui-sm text-muted">Rings your avatar wherever your presence and runs appear.</p>
      <div className="flex flex-wrap items-center gap-1">
        {presetColors.map((color) => (
          <Button key={color} variant="ghost" size="icon" label={`Set colour ${color}`} aria-pressed={current === color} onClick={() => void recolor(color)}>
            <span
              className={cn('size-4 rounded-full', current === color && 'ring-2 ring-text ring-offset-2 ring-offset-raised')}
              style={{ backgroundColor: color }}
            />
          </Button>
        ))}
      </div>
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
    </section>
  )
}

function AccountSharing({ client, self }: { client: Api; self: Member }) {
  const members = useStore((s) => s.members)
  const caps = useCapability()
  const [sharedWith, setSharedWith] = useState<Member[]>([])
  const [sharing, setSharing] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Set by a first share, which only containers created after it honour.
  const [firstShare, setFirstShare] = useState<Member | null>(null)
  const [stopping, setStopping] = useState(false)
  const [terminalUnread, setTerminalUnread] = useState(false)
  // Drops a terminal read answered after a stop: it describes the ended terminal.
  const terminalRead = useRef(0)
  const terminalRunning = useStore((s) => s.envTerminal.status?.running === true)
  const setTerminalStatus = useStore((s) => s.setEnvTerminalStatus)
  const teammates = Object.values(members).filter((m) => !m.pending && m.id !== self.id)

  useEffect(() => {
    let live = true
    client.accountList().then((access) => live && setSharedWith(access.shared_with), () => {})
    return () => {
      live = false
    }
  }, [client])

  const toggle = async (member: Member) => {
    setSharing(member.id)
    setError(null)
    const shared = sharedWith.some((entry) => entry.id === member.id)
    try {
      if (shared) {
        await client.accountRevoke(member.id)
        setFirstShare(null)
      } else {
        await client.accountShare(member.id)
        // The share stands from here on, so the advice must not wait on the reads that follow.
        if (sharedWith.length === 0 && caps.hasWS('terminal')) {
          setFirstShare(member)
          setTerminalUnread(false)
          const read = ++terminalRead.current
          client.terminalStatus().then(
            (status) => terminalRead.current === read && setTerminalStatus(status),
            () => terminalRead.current === read && setTerminalUnread(true),
          )
        }
      }
      setSharedWith((await client.accountList()).shared_with)
      toast.success(shared ? `Account access revoked from ${member.display_name}` : `Account shared with ${member.display_name}`)
    } catch (err) {
      setError(message(err))
    } finally {
      setSharing(null)
    }
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
            ? 'Your environment terminal was started before you shared, so a'
            : 'Your environment terminal could not be checked. If it is open, it was started before you shared, so a'}{' '}
          Claude Code login written there will not reach {firstShare.display_name}&apos;s runs until you stop it and open it again from
          Environment. Runs you already have running keep the mounts they started with until they end.
        </Callout>
      )}
      {error && <Callout tone="failed" role="alert">{error}</Callout>}
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
                <Button size="sm" variant="secondary" disabled={sharing !== null} onClick={() => void toggle(member)}>
                  {sharing === member.id ? 'Saving…' : shared ? 'Revoke access' : 'Share account'}
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
