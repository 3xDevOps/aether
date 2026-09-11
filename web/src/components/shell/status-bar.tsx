import { useEffect, useRef, useState } from 'react'
import { Slot } from '@/components/slots'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { ThemeToggle } from '@/components/theme'
import { Chip, Tooltip } from '@/components/ui/heroui'
import { formatBytes } from '@/lib/format'
import { inModal } from '@/lib/keys'
import type { ConnectionState } from '@/lib/stream'
import type { DiskUsage } from '@/lib/types'
import { cn, focusRing } from '@/lib/utils'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'
import type { UnreachableKind } from '@/store/server'

const desktopStatusQuery = '(min-width: 768px)'
const wideStatusQuery = '(min-width: 1280px)'
const connectionLabel: Record<ConnectionState, string> = {
  connecting: 'Connecting',
  live: 'Live',
  reconnecting: 'Reconnecting',
  offline: 'Offline',
}

// Health reuses the run state tokens: nothing new to keep in sync.
const connectionDot: Record<ConnectionState, string> = {
  connecting: 'bg-state-waiting',
  live: 'bg-state-done',
  reconnecting: 'bg-state-waiting',
  offline: 'bg-state-failed',
}

// Which hop is down decides what an operator does next: a dead local
// network needs wifi or a VPN back, a dead gateway origin needs its process
// restarted, and a dead SSH hop needs the server or the tunnel looked at
// while the gateway keeps retrying on its own.
const unreachableLabel: Record<UnreachableKind, string> = {
  network: 'this computer is offline - reconnect to wifi or your VPN',
  gateway: 'dashboard gateway is gone - restart aether gui',
  server: 'server unreachable over SSH - check the server and network; retrying',
}

/**
 * What is holding the disk, in the order an operator can act on it. The
 * gauge's tooltip joins these for a pointer; the compact status popup lists
 * them, because touch has no tooltip. Run checkouts are garbage-collected after their TTL,
 * transcripts live as long as their run rows, the database is where the
 * event log accumulates, and the bare workspace repos keep every push and
 * run branch. The bar says the disk is filling; this says what is filling
 * it, which is the only version an operator can act on.
 *
 * The repos line is dropped rather than shown as zero when the server
 * predates the component, so an old server reads as silent instead of as a
 * server with no repositories.
 */
function diskLines(disk: DiskUsage): string[] {
  return [
    'Disk: the filesystem holding the data directory',
    `Worktrees ${formatBytes(disk.worktree_bytes)}`,
    `Transcripts ${formatBytes(disk.transcript_bytes)}`,
    `Database ${formatBytes(disk.database_bytes)}`,
    ...(disk.repo_bytes === undefined
      ? []
      : [`Repos ${formatBytes(disk.repo_bytes)}`]),
    `${formatBytes(disk.free_bytes)} free`,
  ]
}

/**
 * What a member who cannot press the update buttons is told while the
 * server updates itself. Both phases end in the same restart, and a
 * restart nobody explained looks like an outage; an admin has the banner
 * instead, which says the same thing with the controls attached.
 */
function ServerUpdateNotice() {
  const isAdmin = useIsAdmin()
  const progress = useStore((s) => s.serverUpdateProgress)
  const pending = useStore((s) => s.serverUpdate?.pending)
  const phase = progress?.phase ?? (pending ? 'scheduled' : undefined)
  if (isAdmin) return null
  const notice =
    phase === 'scheduled'
      ? 'server update scheduled, terminals will reconnect briefly'
      : phase === 'applying' || phase === 'restarting'
        ? 'server update applying, terminals will reconnect briefly'
        : null
  if (!notice) return null
  return (
    // `truncate` can clip this, and the chip takes no `title` of its own, so
    // the span around it carries the whole sentence for a pointer.
    <span
      role="status"
      title={notice}
      className="flex min-h-[22px] min-w-0 shrink items-center break-words whitespace-normal xl:h-[22px] xl:truncate xl:whitespace-nowrap"
    >
      <Chip
        color="warning"
        variant="soft"
        // The state token by hand, the way its neighbour carries
        // needs-attention: HeroUI's warning foreground is amber, and this
        // readout has always been the neutral the rest of the bar uses.
        className="flex min-w-0 shrink items-center bg-state-waiting/15 text-muted-foreground"
      >
        <Chip.Label className="min-w-0 break-words whitespace-normal xl:truncate xl:whitespace-nowrap">
          {notice}
        </Chip.Label>
      </Chip>
    </span>
  )
}

/**
 * The desktop gateway's always-visible entry point: whether this machine
 * has a linked repository, jumping to onboarding until it does and to
 * settings after. Gated on the link.status verb, so the remote gateway
 * shows nothing.
 */
function LocalStatus() {
  const cap = useCapability()
  const link = useStore((s) => s.linkStatus)
  const navigate = useStore((s) => s.navigate)
  if (!cap.hasLocal('link.status')) return null
  const linked = link?.linked === true
  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            onClick={() => {
              navigate(linked ? 'settings' : 'onboarding')
            }}
            className={cn(
              focusRing,
              'flex h-[22px] min-h-[22px] shrink-0 items-center gap-1 rounded-sm px-1 hover:text-foreground coarse:h-11 coarse:min-h-11',
            )}
          >
            <span
              className={cn(
                'size-2 rounded-full',
                linked ? 'bg-state-done' : 'bg-state-waiting',
              )}
              aria-hidden
            />
            {linked ? 'Linked' : 'Not linked'}
          </button>
        )}
      />
      <Tooltip.Content>
        {linked ? `Linked to ${link?.repo}` : 'Link a repository'}
      </Tooltip.Content>
    </Tooltip>
  )
}

/**
 * The version label, with a dot when a newer release is out. The banner is
 * the thing that says what to do about it, so the badge's only job is to
 * bring a dismissed one back - clicking it clears the dismissals rather than
 * navigating anywhere.
 */
function VersionLabel({ version, protocol }: { version: string; protocol: string }) {
  const update = useStore((s) => s.update)
  const isAdmin = useIsAdmin()
  const clearDismissedUpdates = useStore((s) => s.clearDismissedUpdates)
  const label = `aether ${version}`
  const available =
    update !== null &&
    (update.cli.update_available || (update.server_behind && isAdmin))

  if (!available) {
    return (
      <span
        className="flex min-h-[22px] min-w-0 items-center break-words whitespace-normal xl:h-[22px] xl:truncate xl:whitespace-nowrap"
        title={`${label} · protocol ${protocol}`}
      >
        {label}
      </span>
    )
  }

  const latest = update.cli.latest ?? ''
  return (
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <button
            {...triggerProps}
            type="button"
            onClick={() => {
              clearDismissedUpdates()
            }}
            aria-label={`Update available: ${latest}`}
            className={cn(
              focusRing,
              'flex min-h-[22px] min-w-0 shrink items-center gap-1 rounded-sm px-1 break-words whitespace-normal coarse:min-h-11 xl:h-[22px] xl:truncate xl:whitespace-nowrap hover:text-foreground',
            )}
          >
            {label}
            <span className="size-2 rounded-full bg-state-waiting" aria-hidden />
          </button>
        )}
      />
      <Tooltip.Content>
        {latest} is available - show the update banner
      </Tooltip.Content>
    </Tooltip>
  )
}

/**
 * The facts a pointer reads from a tooltip or a `title`, written out. Touch
 * has neither, and the compact popup is the one place in the shell with room
 * for a sentence, so the disk breakdown, the protocol version and what this
 * machine is linked to are rows here rather than hints only a mouse can find.
 */
function StatusFacts({ disk }: { disk?: DiskUsage }) {
  const info = useStore((s) => s.info)
  const cap = useCapability()
  const link = useStore((s) => s.linkStatus)
  const rows = [
    ...(info ? [`Protocol ${info.protocol_version}`] : []),
    ...(cap.hasLocal('link.status') && link?.linked === true
      ? [`Linked to ${link.repo}`]
      : []),
    ...(disk && disk.total_bytes > 0 ? diskLines(disk) : []),
  ]
  if (rows.length === 0) return null
  return (
    <ul className="flex min-w-0 flex-col gap-0.5 border-t border-border pt-1 text-[12px] leading-4 text-muted-foreground">
      {rows.map((row) => (
        <li key={row} className="min-w-0 break-words">
          {row}
        </li>
      ))}
    </ul>
  )
}

// Keep the connection and theme controls present at every width. Readouts use
// a collapsible menu until the wide layout has room for the full row, while
// registered status actions stay beside the theme from the desktop breakpoint.
export function StatusBar() {
  const connection = useStore((s) => s.connection)
  const unreachable = useStore((s) => s.unreachable)
  const info = useStore((s) => s.info)
  const disk = info?.disk
  const [desktop, setDesktop] = useState(
    () => window.matchMedia?.(desktopStatusQuery).matches ?? false,
  )
  const [wide, setWide] = useState(
    () => window.matchMedia?.(wideStatusQuery).matches ?? false,
  )
  const [mobileDetailsOpen, setMobileDetailsOpen] = useState(false)

  useEffect(() => {
    const desktopMedia = window.matchMedia?.(desktopStatusQuery)
    const wideMedia = window.matchMedia?.(wideStatusQuery)
    if (!desktopMedia && !wideMedia) return
    const applyDesktop = (event: MediaQueryListEvent) => setDesktop(event.matches)
    const applyWide = (event: MediaQueryListEvent) => setWide(event.matches)
    desktopMedia?.addEventListener('change', applyDesktop)
    wideMedia?.addEventListener('change', applyWide)
    return () => {
      desktopMedia?.removeEventListener('change', applyDesktop)
      wideMedia?.removeEventListener('change', applyWide)
    }
  }, [])

  const detailsOpen = wide || mobileDetailsOpen
  const statusActions = <Slot name="statusbar" />
  const details = useRef<HTMLDivElement>(null)

  // The compact popup covers the view it sits over, and the only thing that
  // closes it is a 22px trigger at the screen edge. Touch has no hover to
  // find that trigger with, so the popup dismisses the way every other
  // overlay does: a tap or click anywhere else, or Escape. A dialog above it
  // owns Escape first, which is what `inModal` answers. The popup is not a
  // Radix overlay because the status Slot inside it stays mounted while it is
  // closed - a contributor owns a keyboard shortcut of its own.
  useEffect(() => {
    if (wide || !mobileDetailsOpen) return
    const outside = (event: PointerEvent) => {
      if (details.current?.contains(event.target as Node | null)) return
      setMobileDetailsOpen(false)
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented || inModal(event.target)) return
      setMobileDetailsOpen(false)
      // A dismissed popup can be holding the focus; the trigger is where it
      // came from and where it opens again.
      details.current
        ?.querySelector<HTMLElement>('[aria-controls="status-details"]')
        ?.focus()
    }
    window.addEventListener('pointerdown', outside, true)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('pointerdown', outside, true)
      window.removeEventListener('keydown', onKey)
    }
  }, [mobileDetailsOpen, wide])

  return (
    <footer className="relative flex min-h-[22px] shrink-0 items-center gap-1 border-0 bg-sidebar py-0 pr-[max(0.5rem,env(safe-area-inset-right))] pb-[env(safe-area-inset-bottom)] pl-[max(0.5rem,env(safe-area-inset-left))] text-[12px] leading-none text-muted-foreground coarse:min-h-11 before:pointer-events-none before:absolute before:inset-x-0 before:top-0 before:h-px before:bg-border before:content-['']">
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <span className="flex h-[22px] shrink-0 items-center gap-1 coarse:h-11">
          <span
            className={cn('size-1.5 rounded-full', connectionDot[connection])}
            aria-hidden
          />
          {connectionLabel[connection]}
        </span>
        <Collapsible
          ref={details}
          className="relative block h-[22px] min-w-0 leading-none coarse:h-11 xl:flex-1"
          open={detailsOpen}
          onOpenChange={(open) => {
            if (!wide) setMobileDetailsOpen(open)
          }}
        >
          <Tooltip>
            <Tooltip.Trigger<'button'>
              render={(triggerProps) => (
                <CollapsibleTrigger
                  {...triggerProps}
                  className="h-[22px] w-[22px] justify-center rounded-sm border border-transparent text-muted-foreground hover:border-border hover:bg-toolbar-hover hover:text-foreground coarse:h-11 coarse:min-h-11 coarse:w-11 xl:hidden"
                  aria-label="Show status details"
                  aria-controls="status-details"
                />
              )}
            />
            <Tooltip.Content>Show status details</Tooltip.Content>
          </Tooltip>
          <CollapsibleContent
            id="status-details"
            forceMount
            className="block min-w-0 data-[state=closed]:hidden xl:h-[22px] xl:flex-1"
          >
            <div
              className="fixed inset-x-2 bottom-[calc(1.75rem_+_env(safe-area-inset-bottom))] z-50 mb-1 flex max-h-[70dvh] min-w-0 max-w-md flex-col items-stretch gap-1 overflow-y-auto rounded-sm border border-border bg-popover p-2 text-popover-foreground leading-4 shadow-lg coarse:bottom-[calc(3rem_+_env(safe-area-inset-bottom))] xl:static xl:flex xl:h-[22px] xl:w-full xl:min-w-0 xl:max-w-none xl:flex-1 xl:flex-row xl:items-center xl:gap-2 xl:rounded-none xl:border-0 xl:bg-transparent xl:p-0 xl:text-muted-foreground xl:leading-none xl:shadow-none"
            >
              {unreachable !== null && (
                // needs-attention has no HeroUI colour of its own, so the
                // chip carries the state token rather than the nearest
                // stand-in.
                <span
                  role="status"
                  title={unreachableLabel[unreachable]}
                  className="flex min-h-[22px] min-w-0 items-center break-words whitespace-normal xl:h-[22px] xl:truncate xl:whitespace-nowrap"
                >
                  <Chip
                    color="warning"
                    variant="soft"
                    className="flex min-w-0 shrink items-center bg-state-needs-attention/15 text-state-needs-attention"
                  >
                    <Chip.Label className="min-w-0 break-words whitespace-normal xl:truncate xl:whitespace-nowrap">
                      {unreachableLabel[unreachable]}
                    </Chip.Label>
                  </Chip>
                </span>
              )}
              <ServerUpdateNotice />
              {info && (
                <VersionLabel
                  version={info.server_version}
                  protocol={info.protocol_version}
                />
              )}
              <LocalStatus />
              {info && (
                <span
                  title={info.member.display_name}
                  className="flex min-h-[22px] min-w-0 items-center break-words whitespace-normal xl:h-[22px] xl:max-w-40 xl:shrink-0 xl:truncate xl:whitespace-nowrap"
                >
                  {info.member.display_name}
                </span>
              )}
              {disk && disk.total_bytes > 0 && (
                <span
                  className="flex min-h-[22px] min-w-0 items-center gap-1 break-words whitespace-normal xl:h-[22px] xl:shrink xl:truncate xl:whitespace-nowrap"
                  aria-label="Disk usage"
                  title={diskLines(disk).join(' · ')}
                >
                  <span className="h-1.5 w-16 shrink-0 overflow-hidden rounded-sm bg-muted">
                    <span
                      className="block h-full bg-foreground/50"
                      style={{
                        width: `${Math.min(100, (disk.used_bytes / disk.total_bytes) * 100)}%`,
                      }}
                    />
                  </span>
                  <span className="min-w-0 break-words xl:truncate xl:whitespace-nowrap">
                    {formatBytes(disk.used_bytes)} / {formatBytes(disk.total_bytes)}
                  </span>
                </span>
              )}
              {!desktop && (
                <span className="flex min-w-0 flex-wrap items-center gap-1">
                  {statusActions}
                </span>
              )}
              {!wide && <StatusFacts disk={disk} />}
            </div>
          </CollapsibleContent>
        </Collapsible>
      </div>
      <span className="flex min-w-0 shrink-0 items-center gap-2">
        {desktop && (
          <span className="flex min-w-0 shrink-0 items-center gap-2">
            {statusActions}
          </span>
        )}
        <ThemeToggle />
      </span>
    </footer>
  )
}
