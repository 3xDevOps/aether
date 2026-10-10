import { Tooltip as TooltipPrimitive } from 'radix-ui'
import { useLayoutEffect, useRef, useState, type ComponentProps, type ReactElement } from 'react'
import { Avatar } from '@/components/ui/avatar'
import { StatusDot } from '@/components/ui/status-dot'
import { useIsMobile } from '@/lib/breakpoints'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { clipLabel, runLabel, type PresentationState } from '@/lib/status'
import { cn, surface } from '@/lib/utils'
import { modeLabel } from '@/routes/run/agent-name'
import { useStore } from '@/store'
import { isTerminal, type RunRecord } from '@/store/runs'

interface Shown {
  run: RunRecord
  state: PresentationState
  reason: string
  since: string
  /** A swarm parent's worker counts. */
  counts: string
  people: string[]
}

/** How far the box stays from the viewport's edges. */
const edge = 8

/** Rows inside share one delay: the first box waits, the next follows the pointer. */
export function RunDetailsGroup(props: ComponentProps<'div'>) {
  return (
    <TooltipPrimitive.Provider delayDuration={400} disableHoverableContent>
      <div {...props} />
    </TooltipPrimitive.Provider>
  )
}

/** The child is the run's `ListRow`: its button is the trigger and its root is the edge the box docks to. */
export function RunDetails({ label, children, ...shown }: Shown & { label: string; children: ReactElement }) {
  const hover = !useMediaQuery(coarsePointer)
  const mobile = useIsMobile()
  const row = useRef<HTMLButtonElement>(null)
  const [offset, setOffset] = useState(0)
  if (!hover || mobile) return children
  return (
    <TooltipPrimitive.Root
      onOpenChange={(open) => {
        const button = row.current
        // The row's own buttons end the trigger short of the row's edge, where the box docks.
        if (open && button?.parentElement) {
          setOffset(button.parentElement.getBoundingClientRect().right - button.getBoundingClientRect().right)
        }
      }}
    >
      <TooltipPrimitive.Trigger asChild ref={row} onFocus={(event) => event.preventDefault()}>
        {children}
      </TooltipPrimitive.Trigger>
      <TooltipPrimitive.Portal>
        <TooltipPrimitive.Content
          data-slot="run-details"
          aria-label={label}
          side="right"
          align="start"
          sideOffset={offset}
          collisionPadding={edge}
          className={cn(
            surface,
            'pointer-events-none z-50 max-h-(--radix-tooltip-content-available-height) w-72 overflow-hidden rounded-tl-none p-3 text-ui-sm data-[state=delayed-open]:animate-expand-right motion-reduce:animate-none',
          )}
        >
          <Details {...shown} />
        </TooltipPrimitive.Content>
      </TooltipPrimitive.Portal>
    </TooltipPrimitive.Root>
  )
}

const dayAndTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

// The box cannot scroll, so it lists seven lines of people at most. With every other part at its
// tallest under a 120-character title, that fits a 600px window, the desktop shell's minimum, at the
// Default and Large text sizes. Larger text or a shorter window gives lines up in `Details`.
const peopleLines = 7

function Details({ run, state, reason, since, counts, people }: Shown) {
  const self = useStore((s) => s.info?.member.id)
  const members = useStore((s) => s.members)
  const workspace = useStore((s) => s.workspaces[run.workspace_id]?.name ?? run.workspace_id)
  const agent = useStore((s) => s.agentList?.agents.find((a) => a.name === run.harness)?.display_name) || run.harness
  const name = (id: string) => `${members[id]?.display_name ?? id}${id === self ? ' (you)' : ''}`
  const person = (id: string) => (
    <span className="flex min-w-0 items-center gap-1.5">
      <Avatar name={name(id)} color={members[id]?.color} />
      <span className="min-w-0 truncate">{name(id)}</span>
    </span>
  )
  const started = run.started_at ?? run.created_at
  const controller = run.controller_member_id
  const last = run.last_controller_member_id
  // An older gateway sends no controller, which is not the same as nobody.
  const unattended = isTerminal(run.status) ? undefined
    : people.length === 0 ? 'Nobody is on this run'
      : controller === '' ? 'Nobody is controlling'
        : undefined
  // A box too tall for the window gives way one step per layout pass: a line of people at a time,
  // down to a bare count; then the reason, to one line; then the title, to three. The list ends in
  // a count and never in a cut.
  const [squeeze, setSqueeze] = useState(0)
  const content = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const box = content.current?.parentElement
    if (box && squeeze <= peopleLines && box.scrollHeight + box.offsetHeight - box.clientHeight > window.innerHeight - 2 * edge) {
      setSqueeze(squeeze + 1)
    }
  })
  const lines = Math.max(1, peopleLines - squeeze)
  const listed = people.length > lines ? people.slice(0, lines - 1) : people
  const unlisted = people.length - listed.length
  return (
    <div ref={content} className="flex flex-col gap-2.5">
      <div className="flex flex-col gap-1">
        {/* An agent's own title has no length limit; every other title already stops where clipLabel does. */}
        <p className={cn('text-ui font-medium break-words', squeeze > peopleLines && 'line-clamp-3')}>{clipLabel(runLabel(run))}</p>
        <p className="flex gap-1.5">
          <StatusDot tone={state} className="mt-0.75" />
          <span
            className={cn(
              'min-w-0 break-words',
              squeeze < peopleLines ? 'line-clamp-3' : 'line-clamp-1',
              state !== 'needs-you' && 'text-muted',
            )}
          >
            {reason}
          </span>
        </p>
      </div>
      <dl className="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-2 gap-y-1 [&_dt]:text-muted">
        <dt>Workspace</dt>
        <dd className="truncate">{workspace}</dd>
        <dt>Agent</dt>
        <dd className="truncate">{agent} · {modeLabel[run.mode] ?? run.mode}</dd>
        <dt>Branch</dt>
        <dd className="truncate font-code">{run.branch}</dd>
        {counts && (
          <>
            <dt>Swarm</dt>
            <dd>{counts}</dd>
          </>
        )}
        <dt>{run.started_at ? 'Started' : 'Created'}</dt>
        <dd>{dayAndTime.format(new Date(started))}</dd>
        {since !== started && (
          <>
            <dt>{state === 'needs-you' ? 'Waiting since' : isTerminal(run.status) ? 'Finished' : 'Last change'}</dt>
            <dd>{dayAndTime.format(new Date(since))}</dd>
          </>
        )}
        <dt>Owner</dt>
        <dd>{person(run.member_id)}</dd>
      </dl>
      {(people.length > 0 || unattended) && (
        <div className="flex flex-col gap-1 border-t border-seam pt-2.5">
          {people.length > 0 && <p className="font-medium text-muted">On this run</p>}
          {unattended && (
            <p className="text-muted">
              {unattended}
              {!controller && last && <span className="block truncate">{last === self ? 'You' : name(last)} had control last</span>}
            </p>
          )}
          {listed.map((id) => (
            <div key={id} className="flex items-center justify-between gap-2">
              {person(id)}
              {id === controller && <span className="shrink-0 font-medium">Controlling</span>}
            </div>
          ))}
          {unlisted > 0 && <p className="text-muted">{listed.length > 0 ? `and ${unlisted} more` : `${unlisted} people`}</p>}
        </div>
      )}
    </div>
  )
}
