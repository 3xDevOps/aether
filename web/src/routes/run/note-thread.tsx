import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { Avatar } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { FailureCallout } from '@/components/ui/failure-callout'
import { RelativeTime } from '@/components/ui/relative-time'
import { ClampedBody } from '@/components/ui/timeline'
import { memberNameOr, resolveMember } from '@/lib/member-name'
import { playEntrance, type Entrance } from '@/lib/motion'
import type { RoomMessage } from '@/lib/types'
import { cn } from '@/lib/utils'
import type { RunNavigation } from '@/routes/run/header'
import { requestCardID } from '@/routes/run/requests'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'
import { compareInstants, isNote, isProvisional, repliesByQuestion, unansweredQuestions, type FailedNote } from '@/store/collaboration'
import type { RunRecord } from '@/store/runs'

const arrival: Entrance = { motion: { duration: '--duration-360', easing: '--ease-glide' }, rise: 4 }

export function NoteMessage({ authorID, authorName, via, question, at, body, status, reply, arrive, children }: {
  authorID: string
  /** The name the message carried, shown when the author is no longer a member. */
  authorName?: string
  via?: string
  question?: boolean
  at: string
  body: string
  /** Takes the time's place and mutes the body while the note is not saved. */
  status?: string
  reply?: boolean
  arrive?: boolean
  children?: React.ReactNode
}) {
  const member = useStore((s) => resolveMember(s, authorID).member)
  const name = useStore((s) => memberNameOr(s, authorID, authorName))
  const head = useRef<HTMLDivElement>(null)
  const text = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    if (!arrive) return
    playEntrance(head.current!, arrival)
    playEntrance(text.current!, { ...arrival, motion: { ...arrival.motion, delay: '--duration-100' } })
  }, [arrive])
  return (
    <article data-slot="note" className="flex flex-col">
      <div ref={head} className="flex min-h-5 min-w-0 items-center gap-2 text-ui">
        <Avatar name={name} color={member?.color} size={reply ? 'row' : 'header'} />
        <span className="min-w-0 truncate font-medium text-text">
          {name}
          {via && <span className="font-normal text-muted"> via {via}</span>}
        </span>
        {question && <Badge>question</Badge>}
        <span className="ml-auto shrink-0 pl-2 text-ui-sm text-muted tabular-nums">
          {status ?? <RelativeTime at={at} />}
        </span>
      </div>
      <div ref={text} className={cn('mt-1 flex flex-col items-start', reply ? 'pl-6' : 'pl-7')}>
        <ClampedBody className={cn('w-full text-ui', status && 'text-muted')}>{body}</ClampedBody>
        {children}
      </div>
    </article>
  )
}

const noteKey = (m: RoomMessage) => `${m.actor_id}:${m.idempotency_key ?? m.id}`

/**
 * Which notes and replies arrived after the thread settled: not there when the first page loaded and not older history.
 * Each plays its entrance once, whatever later hides or shows it; a teammate's is announced to screen readers.
 */
function useArrivals(arrivals: RoomMessage[], messages: RoomMessage[], ready: boolean, selfID: string | undefined) {
  const baseline = useRef<{ keys: Set<string>; newest?: string }>(undefined)
  const played = useRef(new Set<string>())
  const [announcement, setAnnouncement] = useState('')
  if (ready && !baseline.current) baseline.current = { keys: new Set(arrivals.map(noteKey)), newest: messages.at(-1)?.created_at }
  const arriving = (m: RoomMessage) => {
    const start = baseline.current
    return !!start && !played.current.has(noteKey(m)) && !start.keys.has(noteKey(m)) &&
      (isProvisional(m) || !start.newest || compareInstants(m.created_at, start.newest) > 0)
  }
  useEffect(() => {
    const fresh = arrivals.filter(arriving)
    if (fresh.length === 0) return
    fresh.forEach((m) => played.current.add(noteKey(m)))
    const others = fresh.filter((m) => m.actor_id !== selfID)
    if (others.length === 0) return
    const s = useStore.getState()
    setAnnouncement(
      others.length === 1
        ? `${memberNameOr(s, others[0].actor_id, others[0].actor_display_name)} added a note: ${others[0].body}`
        : `${others.length} new notes`,
    )
  })
  return { arriving, announcement }
}

const recentNotes = 5
const emptyMessages: RoomMessage[] = []
const noFailedNotes: FailedNote[] = []

export function NoteThread({ run, room, nav }: { run: RunRecord; room: RunRoom; nav: RunNavigation }) {
  const runID = run.id
  const messages = useStore((s) => s.roomMessages[runID] ?? emptyMessages)
  const failed = useStore((s) => s.failedNotes[runID] ?? noFailedNotes)
  const pagination = useStore((s) => s.roomPagination[runID])
  const loading = useStore((s) => s.roomLoading[runID] === true)
  const error = useStore((s) => s.roomError[runID])
  const selfID = useStore((s) => s.info?.member.id)
  const [all, setAll] = useState(false)
  const [olderFailed, setOlderFailed] = useState(false)
  const notes = useMemo(() => messages.filter(isNote), [messages])
  const replies = useMemo(() => repliesByQuestion(messages), [messages])
  const awaiting = useMemo(
    () => new Set(unansweredQuestions(messages).filter((m) => m.actor_id !== run.member_id).map((m) => m.id)),
    [messages, run.member_id],
  )
  const threaded = useMemo(() => messages.filter((m) => isNote(m) || m.kind === 'reply'), [messages])
  const { arriving, announcement } = useArrivals(threaded, messages, pagination?.initialized === true, selfID)
  const shown = all ? notes : notes.slice(-recentNotes)
  const earlier = notes.length - shown.length
  const canLoadOlder = earlier === 0 && pagination?.exhausted !== true
  const showOlder = async () => {
    setAll(true)
    await room.loadOlder()
    setOlderFailed(useStore.getState().roomError[runID] !== undefined)
  }
  const retry = () => {
    if (olderFailed) return void showOlder()
    setOlderFailed(false)
    void room.refresh()
  }

  return (
    <>
      <p role="status" className="sr-only">{announcement}</p>
      {error && (
        <div role="alert" className="flex items-start gap-2 text-ui-sm text-state-failed">
          <span className="min-w-0 flex-1 break-words">{error}</span>
          <Button size="sm" variant="ghost" onClick={retry}>Retry</Button>
        </div>
      )}
      {earlier > 0 && (
        <Button size="sm" variant="quiet" className="self-start" onClick={() => setAll(true)}>
          Show {earlier} earlier {earlier === 1 ? 'note' : 'notes'}
        </Button>
      )}
      {canLoadOlder && pagination?.initialized && (
        <Button size="sm" variant="quiet" className="self-start" disabled={loading} onClick={() => void showOlder()}>
          {loading ? 'Loading…' : 'Show older'}
        </Button>
      )}
      {shown.length > 0 && (
        <ol className="flex flex-col gap-4">
          {shown.map((m) => (
            <li key={noteKey(m)}>
              <NoteMessage
                authorID={m.actor_id}
                authorName={m.actor_display_name}
                via={m.via}
                question={m.kind === 'question'}
                at={m.created_at}
                body={m.body}
                status={isProvisional(m) ? 'Sending…' : undefined}
                arrive={arriving(m)}
              >
                {awaiting.has(m.id) && (
                  <Button variant="link" size="sm" className="mt-1" onClick={() => nav.reveal(requestCardID.question(m.id))}>Reply</Button>
                )}
                {replies.get(m.id)?.map((r) => (
                  <div key={r.id} className="relative mt-3 w-full">
                    <span aria-hidden className="absolute -top-3 -left-[18px] h-[22px] w-3.5 rounded-bl-chip border-b border-l border-seam" />
                    <NoteMessage authorID={r.actor_id} authorName={r.actor_display_name} via={r.via} at={r.created_at} body={r.body} reply arrive={arriving(r)} />
                  </div>
                ))}
              </NoteMessage>
            </li>
          ))}
        </ol>
      )}
      {failed.map((note) => (
        <NoteMessage key={note.key} authorID={selfID ?? ''} at="" body={note.text} status="Not sent">
          <div className="mt-1 w-full">
            <FailureCallout
              error={note.error}
              onRetry={note.unanswered ? () => void room.note(note.text) : undefined}
              onDismiss={() => useStore.getState().dismissFailedNote(runID, note.key)}
            />
          </div>
        </NoteMessage>
      ))}
    </>
  )
}
