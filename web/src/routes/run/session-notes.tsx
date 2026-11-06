import { useEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { Check, ChevronDown, X } from '@/components/icons'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { FailureCallout } from '@/components/ui/failure-callout'
import { RelativeTime } from '@/components/ui/relative-time'
import { ClampedBody } from '@/components/ui/timeline'
import { prefersReducedMotion } from '@/lib/hooks'
import { memberNameOr, resolveMember } from '@/lib/member-name'
import { play, type Motion } from '@/lib/motion'
import type { RoomMessage, RoomResolution } from '@/lib/types'
import { cn } from '@/lib/utils'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { byCreated, canResolveNote, isOpenNote } from '@/store/collaboration'
import type { RunRecord } from '@/store/runs'

/** The 736px Chat column plus a 256px card slot (236px card, 20px edge) on each
 * side. Below this frame width, or whenever Details shrinks it past this, notes
 * move from the rail into a strip above the composer. */
export const notesRailMinWidth = 1248

const scratchMotion: Motion = { duration: '--duration-450', easing: '--ease-glide' }
const doneExitMotion: Motion = { duration: '--duration-300', easing: '--ease-glide' }
const deleteExitMotion: Motion = { duration: '--duration-200', easing: '--ease-standard' }
const reducedExitMotion: Motion = { duration: '--duration-150', easing: '--ease-standard' }
const rotations = ['-0.6deg', '0.7deg']

const emptyMessages: RoomMessage[] = []

type NoteError = { message: string; outcome: RoomResolution }

/** Open comments, oldest first: the stack order the rail and strip share. */
function openComments(messages: RoomMessage[]): RoomMessage[] {
  return byCreated(messages.filter(isOpenNote))
}

/**
 * Keeps a note mounted after it leaves `open` until both its exit animation
 * and the server's answer are in, so a refusal can still show its error on
 * the card. A deleted note leaves the store at once, so the rail keeps the
 * last copy it saw.
 */
function useRailOrder(open: RoomMessage[]) {
  // Per exiting note: how many of the animation and the answer are still due.
  const exiting = useRef<Map<string, number>>(new Map())
  const openRef = useRef<RoomMessage[]>(open)
  openRef.current = open
  const [notes, setNotes] = useState<RoomMessage[]>([])
  const [errors, setErrors] = useState<Record<string, NoteError>>({})
  useEffect(() => {
    const byID = new Map(open.map((m) => [m.id, m]))
    setNotes((prev) => {
      const kept = prev.filter((m) => byID.has(m.id) || exiting.current.has(m.id)).map((m) => byID.get(m.id) ?? m)
      const added = open.filter((m) => !kept.some((k) => k.id === m.id))
      return added.length === 0 && kept.length === prev.length && kept.every((m, i) => m === prev[i]) ? prev : [...kept, ...added]
    })
  }, [open])
  const setError = (id: string, error: NoteError | undefined) =>
    setErrors((prior) => {
      if (error === undefined) {
        if (!(id in prior)) return prior
        const { [id]: _drop, ...rest } = prior
        return rest
      }
      return { ...prior, [id]: error }
    })
  // Drops the row only if it is still resolved in the store, so a reverted
  // refusal leaves it in place.
  const clearExiting = (id: string) => {
    exiting.current.delete(id)
    setNotes((prev) => {
      if (openRef.current.some((m) => m.id === id)) return prev
      return prev.filter((m) => m.id !== id)
    })
  }
  return {
    notes,
    errors,
    markExiting: (id: string) => exiting.current.set(id, 2),
    setError,
    /** The exit animation or the server's acceptance is in. */
    settle: (id: string) => {
      const due = (exiting.current.get(id) ?? 1) - 1
      if (due > 0) exiting.current.set(id, due)
      else clearExiting(id)
    },
    clearExiting,
  }
}

function ScratchStrokes({ draw }: { draw: React.RefObject<SVGSVGElement | null> }) {
  return (
    <svg ref={draw} aria-hidden className="pointer-events-none absolute inset-0 h-full w-full" viewBox="0 0 100 60" preserveAspectRatio="none">
      {[14, 32, 48].map((y, i) => (
        <path
          key={y}
          d={`M2 ${y} Q 50 ${y + (i % 2 === 0 ? 4 : -4)} 98 ${y}`}
          fill="none"
          stroke="var(--scratch-stroke)"
          strokeWidth={2.2}
          strokeLinecap="round"
          style={{ opacity: 0 }}
        />
      ))}
    </svg>
  )
}

function StickyCard({ note, index, run, error, retryable, onResolve, onDismissError, onSettled }: {
  note: RoomMessage
  index: number
  run: RunRecord
  error: NoteError | undefined
  /** The note is still open, so a resend can succeed. */
  retryable: boolean
  onResolve: (outcome: RoomResolution) => void
  onDismissError: () => void
  /** The card's exit animation has finished and it may be unmounted. */
  onSettled: () => void
}) {
  const member = useStore((s) => resolveMember(s, note.actor_id).member)
  const name = useStore((s) => memberNameOr(s, note.actor_id, note.actor_display_name))
  const selfID = useStore((s) => s.info?.member.id)
  const selfRole = useStore((s) => s.info?.member.role ?? null)
  const may = canResolveNote({ id: selfID, role: selfRole }, note, run.member_id)
  const frame = useRef<HTMLLIElement>(null)
  const scratch = useRef<SVGSVGElement>(null)
  const started = useRef(false)
  // Bumped by a refusal; an exit from an earlier generation stops at its next step.
  const generation = useRef(0)

  // A refusal reaches this same card mid-exit or already collapsed: stop the
  // exit and undo the scratch and the collapse so the card shows its error.
  useEffect(() => {
    if (!error || !started.current) return
    started.current = false
    generation.current += 1
    const el = frame.current
    if (!el) return
    const paths = Array.from(el.querySelectorAll('path'))
    for (const node of [el, ...paths]) node.getAnimations().forEach((a) => a.cancel())
    for (const path of paths) path.style.opacity = '0'
    el.style.height = ''
    el.style.overflow = ''
  }, [error])

  const runExit = async (outcome: RoomResolution) => {
    const el = frame.current
    if (!el) return onSettled()
    const current = generation.current
    const live = () => generation.current === current
    if (prefersReducedMotion()) {
      await play(el, [{ opacity: 1 }, { opacity: 0 }], reducedExitMotion, { fill: 'forwards' }).finished.catch(() => {})
      if (live()) onSettled()
      return
    }
    if (outcome === 'done' && scratch.current) {
      const paths = Array.from(scratch.current.querySelectorAll('path'))
      await Promise.all(paths.map((path, i) => {
        const length = path.getTotalLength()
        path.style.strokeDasharray = `${length}`
        path.style.strokeDashoffset = `${length}`
        path.style.opacity = '1'
        return play(path, [{ strokeDashoffset: length }, { strokeDashoffset: 0 }], scratchMotion, { extraDelay: i * 60, fill: 'forwards' }).finished
      })).catch(() => {})
      if (!live()) return
    }
    const height = el.getBoundingClientRect().height
    el.style.overflow = 'clip'
    const motion = outcome === 'done' ? doneExitMotion : deleteExitMotion
    const animations = [
      play(el, [{ opacity: 1 }, { opacity: 0 }], motion, { fill: 'forwards' }),
      play(el, [{ height: `${height}px` }, { height: '0px' }], motion, { fill: 'forwards' }),
    ]
    await Promise.all(animations.map((a) => a.finished)).catch(() => {})
    if (live()) onSettled()
  }

  const act = (outcome: RoomResolution) => {
    if (started.current) return
    started.current = true
    onResolve(outcome)
    void runExit(outcome)
  }

  return (
    <li
      ref={frame}
      className={cn(
        'sticky-note group/note relative flex w-[236px] flex-col gap-1.5 overflow-hidden rounded-[6px] border p-3 shadow-[var(--card-shadow)]',
        error && 'border-state-failed',
      )}
      style={{ transform: `rotate(${rotations[index % 2]})` }}
    >
      <ScratchStrokes draw={scratch} />
      <div className="flex min-w-0 items-center gap-2 text-ui">
        <Avatar name={name} color={member?.color} size="header" />
        <span className="sticky-note__header min-w-0 truncate font-medium">{name}</span>
        <RelativeTime at={note.created_at} className="ml-auto shrink-0 pl-2 text-ui-sm tabular-nums opacity-80" />
        {may && (
          <div className="flex shrink-0 items-center gap-0.5 opacity-0 group-focus-within/note:opacity-100 group-hover/note:opacity-100 coarse:opacity-100">
            <Button variant="ghost" size="icon-sm" label="Complete note" onClick={() => act('done')}><Check /></Button>
            <Button variant="ghost" size="icon-sm" label="Delete note" onClick={() => act('deleted')}><X /></Button>
          </div>
        )}
      </div>
      <ClampedBody className="text-ui">{note.body}</ClampedBody>
      {error && (
        <FailureCallout
          error={error.message}
          onRetry={retryable ? () => { started.current = false; act(error.outcome) } : undefined}
          onDismiss={() => { started.current = false; onDismissError() }}
        />
      )}
    </li>
  )
}

function useStickyNotes(run: RunRecord, room: RunRoom) {
  const messages = useStore((s) => s.roomMessages[run.id] ?? emptyMessages)
  const open = useMemo(() => openComments(messages), [messages])
  const rail = useRailOrder(open)
  const [announcement, setAnnouncement] = useState('')
  const resolve = (note: RoomMessage, outcome: RoomResolution) => {
    rail.markExiting(note.id)
    rail.setError(note.id, undefined)
    void room.resolve(note.id, outcome).then((result) => {
      if (!result.ok) return rail.setError(note.id, { message: result.error ?? 'The server refused.', outcome })
      setAnnouncement(outcome === 'done' ? 'Note completed.' : 'Note deleted.')
      rail.settle(note.id)
    })
  }
  const cards = rail.notes.map((note, index) => (
    <StickyCard
      key={note.id}
      note={note}
      index={index}
      run={run}
      error={rail.errors[note.id]}
      retryable={open.some((m) => m.id === note.id)}
      onResolve={(outcome) => resolve(note, outcome)}
      onDismissError={() => {
        rail.setError(note.id, undefined)
        rail.clearExiting(note.id)
      }}
      onSettled={() => rail.settle(note.id)}
    />
  ))
  // The live region stays mounted after the last note leaves, or its
  // announcement would never be read.
  const status = <span role="status" aria-live="polite" className="sr-only">{announcement}</span>
  return { count: rail.notes.length, cards, status }
}

/** The floating rail in the Chat view's right gutter, wide layouts only. */
export function NotesRail({ run, room }: { run: RunRecord; room: RunRoom }) {
  const { count, cards, status } = useStickyNotes(run, room)
  return (
    <>
      {count > 0 && <ol aria-label="Notes" className="absolute top-5 right-5 z-10 flex w-[236px] flex-col gap-3">{cards}</ol>}
      {status}
    </>
  )
}

/** A collapsible strip above the composer, narrow layouts only. */
export function NotesStrip({ run, room, className }: { run: RunRecord; room: RunRoom; className?: string }) {
  const { count, cards, status } = useStickyNotes(run, room)
  const [expanded, setExpanded] = useState(false)
  return (
    <>
      {count > 0 && (
        <div className={cn('flex flex-col gap-2', className)}>
          <Button variant="quiet" size="sm" className="self-start gap-1" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>
            {count} {count === 1 ? 'note' : 'notes'}
            <ChevronDown className={cn('transition-transform', expanded && 'rotate-180')} />
          </Button>
          {expanded && <ol aria-label="Notes" className="flex flex-wrap gap-3">{cards}</ol>}
        </div>
      )}
      {status}
    </>
  )
}
