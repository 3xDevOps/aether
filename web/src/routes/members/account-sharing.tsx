import { type ReactNode, useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FailureCallout } from '@/components/ui/failure-callout'
import type { Api } from '@/lib/api'
import type { Member } from '@/lib/types'
import { StopEnvironmentDialog } from '@/routes/environment/stop-environment-dialog'
import { useFailure } from '@/components/failure'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { optimistic } from '@/store/optimistic'

export interface AccountSharing {
  /** Whether member may launch on the viewer's agent account. */
  shares: (member: string) => boolean
  /** Whether member shares their agent account with the viewer. */
  sharesWithMe: (member: string) => boolean
  pending: ReadonlySet<string>
  toggle: (member: Member) => void
  /** The first-share advice and failed changes, shown above the roster. */
  notices: ReactNode
}

/** The viewer's agent account shares, read once and changed one teammate at a time. */
export function useAccountSharing(client: Api): AccountSharing {
  const caps = useCapability()
  const [sharedWith, setSharedWith] = useState<Member[]>([])
  const [owners, setOwners] = useState<Member[]>([])
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

  // Teammates shared or revoked since the list read was sent keep what is shown: the read may predate the change.
  const changed = useRef(new Set<string>())
  // Teammates the server holds a grant for, as far as the list read and answers since tell.
  const saved = useRef(new Set<string>())

  useEffect(() => {
    if (!caps.hasMethod('account.list')) return
    let live = true
    const since = new Set<string>()
    changed.current = since
    client.accountList().then(
      (access) => {
        if (!live) return
        const read = access.shared_with.filter((m) => !since.has(m.id))
        saved.current = new Set([...read.map((m) => m.id), ...[...saved.current].filter((id) => since.has(id))])
        setSharedWith((list) => [...read, ...list.filter((m) => since.has(m.id))])
        setOwners(access.accounts)
      },
      () => {},
    )
    return () => {
      live = false
    }
  }, [caps, client])

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
    }), { key: member.id })
  }

  const notices = (
    <>
      {firstShare && (terminalRunning || terminalUnread) && (
        <Callout
          tone="needs-you"
          role="status"
          actions={<Button size="sm" variant="secondary" onClick={() => setStopping(true)}>Stop environment</Button>}
        >
          {terminalRunning
            ? 'Your Environment was started before you shared, so a'
            : 'Your Environment could not be checked. If it is open, it was started before you shared, so a'}{' '}
          Claude Code login written there will not reach {firstShare.display_name}&apos;s sessions until you stop it and open it again from
          Environment. Sessions you already have running keep the mounts they started with until they end.
        </Callout>
      )}
      {failures.map((failure) => <FailureCallout key={failure.id} error={failure.error} onRetry={failure.retry} />)}
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
    </>
  )

  return {
    shares: (id) => sharedWith.some((m) => m.id === id),
    sharesWithMe: (id) => owners.some((m) => m.id === id),
    pending,
    toggle,
    notices,
  }
}

export function AccountSharingNote() {
  return (
    <div className="flex min-w-0 flex-col">
      <p className="text-ui-sm text-muted">
        <span className="text-text">Share my account</span> lets a teammate launch sessions on your agent logins and quota. The sessions they start stay theirs.
      </p>
      <Collapsible>
        <CollapsibleTrigger><span className="text-muted">What a teammate can do with your account</span></CollapsibleTrigger>
        <CollapsibleContent className="flex flex-col gap-2 pt-1 pl-4">
          <p className="text-ui-sm text-muted">Their sessions can use, refresh, replace or log out your agent logins. Your environment, files and GitHub login are not shared, and running agents are not stopped when you stop sharing.</p>
          <p className="text-ui-sm text-muted">Except omp: an omp share hands over your whole omp agent directory, where their sessions can plant code your own omp conversations run with your home. Share omp only with someone you would give your home to.</p>
        </CollapsibleContent>
      </Collapsible>
    </div>
  )
}
