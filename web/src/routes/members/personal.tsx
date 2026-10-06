import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/tooltip'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Member } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { MemberAvatar } from '@/routes/board/member-avatar'
import { StopEnvironmentDialog } from '@/routes/environment/stop-environment-dialog'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'

// Passed to member.color as data; the inline style below is the sanctioned member-colour exception.
const presetColors = [
  '#e6194b',
  '#3cb44b',
  '#f58231',
  '#911eb4',
  '#46f0f0',
  '#4363d8',
]

export function PersonalSections({ client = api }: { client?: Api }) {
  const members = useStore((s) => s.members)
  const setMembers = useStore((s) => s.setMembers)
  const self = useStore((s) => s.info?.member)
  const caps = useCapability()
  const [error, setError] = useState<string | null>(null)
  const [sharedWith, setSharedWith] = useState<Member[]>([])
  const [sharing, setSharing] = useState<string | null>(null)
  // Set by a first share, which only containers created after it honour.
  const [firstShare, setFirstShare] = useState<Member | null>(null)
  const [stoppingTerminal, setStoppingTerminal] = useState(false)
  const [terminalUnread, setTerminalUnread] = useState(false)
  // Drops a terminal read answered after a stop: it describes the ended terminal.
  const terminalRead = useRef(0)
  const terminalRunning = useStore((s) => s.envTerminal.status?.running === true)
  const setTerminalStatus = useStore((s) => s.setEnvTerminalStatus)
  const roster = Object.values(members).filter((m) => !m.pending)

  useEffect(() => {
    let cancelled = false
    client
      .accountList()
      .then((access) => {
        if (!cancelled) setSharedWith(access.shared_with)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [client])

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
        setFirstShare(null)
      } else {
        await client.accountShare(member.id)
        // The share is granted from here on, so the advice must not depend
        // on the reads that follow succeeding.
        if (sharedWith.length === 0 && caps.hasWS('terminal')) {
          setFirstShare(member)
          setTerminalUnread(false)
          const read = ++terminalRead.current
          client.terminalStatus().then(
            (status) => {
              if (terminalRead.current === read) setTerminalStatus(status)
            },
            () => {
              if (terminalRead.current === read) setTerminalUnread(true)
            },
          )
        }
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

  const recolor = async (color: string) => {
    setError(null)
    try {
      await client.memberColor(color)
      await refetch()
    } catch (err) {
      setError(message(err))
    }
  }

  return (
    <>
      {error && (
        <p role="alert" className="text-ui text-state-failed">
          {error}
        </p>
      )}
      {self &&
        caps.hasMethod('account.share') &&
        caps.hasMethod('account.revoke') && (
          <section aria-label="Account sharing" className="space-y-2">
            <div className="min-w-0 border-b pb-1">
              <h2 className="text-[13px] font-semibold">Your agent account</h2>
              <p className="mt-1 max-w-2xl text-xs leading-4 text-muted-foreground">
                Choose teammates who may launch runs with your account. This is
                separate from roster roles and can be changed at any time.
              </p>
            </div>
            <ul className="border-y border-border text-xs text-muted-foreground">
              <li className="border-b border-border px-3 py-1.5">
                Your agent logins and vendor quota are shared, and their runs can
                use, refresh, replace, or log out those logins. Your environment,
                files, and GitHub login are not.
              </li>
              <li className="border-b border-border px-3 py-1.5">
                Except omp: an omp share hands over your whole ~/.omp/agent, where
                their runs can plant code that your own omp sessions run with your
                home. Share omp only with someone you would give your home to.
              </li>
              <li className="px-3 py-1.5">
                Their runs remain attributed to them; running agents are not stopped.
              </li>
            </ul>
            {firstShare && (terminalRunning || terminalUnread) && (
              <div
                role="status"
                className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-l-2 border-state-needs-attention bg-state-needs-attention/10 px-3 py-2 text-xs"
              >
                <p className="min-w-0 max-w-2xl">
                  {terminalRunning
                    ? 'Your environment terminal was started before you shared, so a'
                    : 'Your environment terminal could not be checked. If it is open, it was started before you shared, so a'}{' '}
                  Claude Code login written there will not reach{' '}
                  {firstShare.display_name}&apos;s runs until you stop it and open it
                  again from Environment. Runs you already have
                  running keep the mounts they started with until they end.
                </p>
                <Button
                  size="md"
                  variant="secondary"
                  onClick={() => setStoppingTerminal(true)}
                >
                  Stop environment
                </Button>
              </div>
            )}
            <ul className="divide-y border-b border-border">
              {roster
                .filter((member) => member.id !== self.id)
                .map((member) => {
                  const shared = sharedWith.some(
                    (entry) => entry.id === member.id,
                  )
                  return (
                    <li
                      key={member.id}
                      className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 px-3 py-2"
                    >
                      <MemberAvatar
                        member={member}
                        fallback={member.display_name}
                        className="size-6"
                      />
                      <span className="min-w-0 break-words text-[13px] font-medium">
                        {member.display_name}
                      </span>
                      <span className="flex min-w-0 flex-wrap items-center justify-end gap-1.5">
                        <Badge tone={shared ? 'done' : 'neutral'} className="hidden sm:flex">
                          {shared ? 'Shared' : 'Not shared'}
                        </Badge>
                        <Button
                          size="md"
                          variant={shared ? 'secondary' : 'primary'}
                          disabled={sharing !== null}
                          onClick={() => void toggleAccountShare(member)}
                        >
                          {sharing === member.id
                            ? 'Saving...'
                            : shared
                              ? 'Revoke access'
                              : 'Share account'}
                        </Button>
                      </span>
                    </li>
                  )
                })}
            </ul>
          </section>

        )}
      {self && caps.hasMethod('member.color') && (
        <section aria-label="Your color" className="space-y-2">
          <div className="border-b pb-1">
            <h2 className="text-[13px] font-semibold">Your color</h2>
            <p className="mt-1 text-xs text-muted-foreground">
              Used for your presence and activity attribution.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2 border-y border-border py-2">
            {presetColors.map((color) => (
              <Tooltip key={color} content={self.color === color ? `Current color ${color}` : `Set color ${color}`}>
                <button
                  type="button"
                  aria-label={`Set color ${color}`}
                  aria-pressed={self.color === color}
                  className={cn(
                    focusRing,
                    'size-8 rounded-full border-2 border-background ring-1 ring-border/70 transition-[box-shadow,transform] hover:scale-105 hover:ring-2 aria-pressed:ring-2 motion-reduce:transition-none',
                  )}
                  style={{ backgroundColor: color }}
                  onClick={() => {
                    void recolor(color)
                  }}
                />
              </Tooltip>
            ))}
            <Badge className="ml-1">
              {self.color}
            </Badge>
          </div>
        </section>
      )}
      {stoppingTerminal && (
        <StopEnvironmentDialog
          client={client}
          onClose={() => setStoppingTerminal(false)}
          onStopped={() => {
            terminalRead.current++
            setFirstShare(null)
          }}
        />
      )}
    </>
  )
}
