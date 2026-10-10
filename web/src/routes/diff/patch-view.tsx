import { Fragment, memo, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { VList } from 'virtua'
import { ChevronDown, ChevronRight, MessageSquare } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { LineGutter } from '@/components/ui/line-gutter'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { cn } from '@/lib/utils'
import { CommentCard } from '@/routes/diff/comment-card'
import { linePrefix, type FileStatus, type PatchFile, type PatchLine } from '@/routes/diff/parse'
import { addComment, commentable, growComment, placeComments, reach, useFileComments, written, type ReviewComment } from '@/routes/diff/review'

export const largeFile = 500

export function contentLines(file: PatchFile): number {
  return file.lines.filter((line) => line.kind !== 'hunk' && line.kind !== 'meta').length
}

const statusWord: Record<FileStatus, string> = {
  added: 'added',
  deleted: 'deleted',
  modified: '',
  binary: 'binary',
}

const lineTint = {
  add: 'bg-diff-add-bg',
  del: 'bg-diff-del-bg',
  hunk: 'bg-chrome text-muted',
  meta: 'text-muted',
  context: '',
}

export function Counts({ additions, deletions }: { additions: number; deletions: number }) {
  return (
    <span className="shrink-0 tabular-nums">
      <span className="text-diff-add">+{additions}</span> <span className="text-diff-del">−{deletions}</span>
    </span>
  )
}

/** The run whose review a file's lines join, and the diff on screen: '' for the current one, else an interval key. */
export interface ReviewTarget {
  runID: string
  scope: string
}

/** Without `onCollapsedChange` the file is always open and has no chevron. With `review` its lines take comments. */
export const FilePatch = memo(function FilePatch({
  file,
  wrap,
  lineNumbers = false,
  collapsed = false,
  onCollapsedChange,
  onOpen,
  id,
  review,
}: {
  file: PatchFile
  wrap: boolean
  lineNumbers?: boolean
  collapsed?: boolean
  onCollapsedChange?: (path: string, collapsed: boolean) => void
  onOpen?: (path: string) => void
  id?: string
  review?: ReviewTarget
}) {
  const open = !onCollapsedChange || !collapsed
  const body = id ? `${id}-body` : undefined
  const gutter = lineNumbers ? `calc(${Math.max(String(lastLine(file)).length, 2)}ch + 1rem)` : undefined
  const size = contentLines(file)
  const runID = review?.runID ?? ''
  const comments = useFileComments(runID, file.path)
  const picker = useLinePicker(file, review, comments)
  const noted = written(comments).length

  const thread = (cards: React.ReactNode) => (
    <div className={cn('flex flex-col gap-2 border-y border-seam bg-chrome p-2 font-sans whitespace-normal first:border-t-0', !wrap && 'sticky left-0 w-[100cqw]')}>
      {cards}
    </div>
  )
  const row = (line: PatchLine, index: number) => {
    const pinned = picker.threads.get(index)
    return (
      <Fragment key={index}>
        <Line
          line={line}
          index={index}
          wrap={wrap}
          gutter={gutter}
          review={review !== undefined}
          marked={picker.marked.has(index)}
          tab={index === picker.tab}
        />
        {pinned && thread(pinned.map(({ comment, start, end }) => (
          <CommentCard
            key={comment.id}
            runID={runID}
            comment={comment}
            lines={file.lines.slice(start, end + 1)}
            onRemoved={() => picker.focusLine(end)}
          />
        )))}
      </Fragment>
    )
  }

  return (
    <section id={id} aria-label={file.path} className="border-b border-seam">
      <header className="sticky top-0 z-10 flex h-8 min-w-0 items-center gap-2 border-b border-seam bg-chrome px-2 text-ui-sm coarse:h-11">
        {onCollapsedChange && (
          <Button
            variant="ghost"
            size="icon-sm"
            label={open ? `Collapse ${file.path}` : `Expand ${file.path}`}
            aria-expanded={open}
            aria-controls={body}
            onClick={() => onCollapsedChange(file.path, open)}
          >
            {open ? <ChevronDown /> : <ChevronRight />}
          </Button>
        )}
        {onOpen ? (
          <Button variant="ghost" size="sm" hint="Open in Files" onClick={() => onOpen(file.path)} className="min-w-0 shrink">
            <span className="truncate font-code text-text">{file.path}</span>
          </Button>
        ) : (
          <span className="min-w-0 truncate px-2 font-code text-text coarse:px-3" title={file.path}>
            {file.path}
          </span>
        )}
        {statusWord[file.status] && <span className="shrink-0 text-muted">{statusWord[file.status]}</span>}
        <span className="ml-auto flex shrink-0 items-center gap-3">
          {noted > 0 && (
            <span className="flex items-center gap-1 text-muted tabular-nums">
              <MessageSquare aria-hidden className="size-3.5" />
              {noted}
              <span className="sr-only">{noted === 1 ? ' comment' : ' comments'}</span>
            </span>
          )}
          {!open && size > largeFile && (
            <span className="text-muted tabular-nums">{size} lines</span>
          )}
          <Counts additions={file.additions} deletions={file.deletions} />
        </span>
      </header>
      {open && (
        <div
          id={body}
          ref={picker.body}
          className={cn('font-code text-ui-sm', review && '@container', !wrap && 'overflow-x-auto overscroll-x-contain', picker.picking && 'select-none')}
          {...picker.handlers}
        >
          {picker.outdated.length > 0 && thread(picker.outdated.map((comment) => (
            <CommentCard key={comment.id} runID={runID} comment={comment} lines={comment.lines} outdated />
          )))}
          {size > largeFile ? (
            <VList data-slot="patch-lines" style={{ height: 'min(70dvh, 40rem)' }} data={file.lines}>
              {row}
            </VList>
          ) : (
            <div data-slot="patch-lines" className={cn(!wrap && 'w-max min-w-full')}>
              {file.lines.map(row)}
            </div>
          )}
        </div>
      )}
    </section>
  )
})

interface Picked {
  anchor: number
  head: number
}

function lineOf(target: EventTarget): number {
  return Number((target as HTMLElement).closest<HTMLElement>('[data-line]')?.dataset.line ?? -1)
}

function inGutter(target: EventTarget): boolean {
  return (target as HTMLElement).closest('[data-slot="line-gutter"]') !== null
}

/**
 * Picks lines to comment on. A click on a gutter opens an editor on that
 * line; a drag, Shift with the arrow keys, or a Shift-click while an editor
 * is open makes it a range. A range never leaves its hunk.
 */
function useLinePicker(file: PatchFile, review: ReviewTarget | undefined, comments: ReviewComment[]) {
  const coarse = useMediaQuery(coarsePointer)
  const body = useRef<HTMLDivElement>(null)
  const [picked, setPicked] = useState<Picked | null>(null)
  const live = useRef<Picked | null>(null)
  const dragging = useRef(false)
  const [cursor, setCursor] = useState(-1)
  const scope = review?.scope ?? ''
  const { placed, outdated } = useMemo(() => placeComments(file.lines, comments, scope), [file.lines, comments, scope])
  const latest = useRef({ file, placed, review })
  latest.current = { file, placed, review }

  const threads = useMemo(() => {
    const at = new Map<number, typeof placed>()
    for (const entry of placed) at.set(entry.end, [...(at.get(entry.end) ?? []), entry])
    return at
  }, [placed])
  const marked = useMemo(() => {
    const lines = new Set<number>()
    const span = (a: number, b: number) => {
      for (let at = Math.min(a, b); at <= Math.max(a, b); at++) lines.add(at)
    }
    for (const entry of placed) span(entry.start, entry.end)
    if (picked) span(picked.anchor, picked.head)
    return lines
  }, [placed, picked])

  const pick = (next: Picked | null) => {
    live.current = next
    setPicked(next)
  }
  const gutterAt = (index: number) =>
    body.current?.querySelector<HTMLElement>(`[data-line="${index}"] > [data-slot="line-gutter"]`)
  const comment = ({ anchor, head }: Picked) => {
    const { file: current, review: target } = latest.current
    if (!target) return
    addComment(target.runID, {
      path: current.path,
      scope: target.scope,
      lines: current.lines.slice(Math.min(anchor, head), Math.max(anchor, head) + 1),
    })
  }

  const handlers: React.HTMLAttributes<HTMLDivElement> | undefined = review && {
    onPointerDown: (event) => {
      if (event.button !== 0 || event.pointerType === 'touch' || !inGutter(event.target)) return
      const index = lineOf(event.target)
      dragging.current = true
      pick({ anchor: index, head: index })
      window.addEventListener('pointerup', () => {
        const range = live.current
        dragging.current = false
        pick(null)
        if (range && range.head !== range.anchor) comment(range)
      }, { once: true })
    },
    onPointerOver: (event) => {
      const range = live.current
      if (!range || !dragging.current) return
      const index = lineOf(event.target)
      if (index < 0) return
      const head = reach(latest.current.file.lines, range.anchor, index)
      if (head !== range.head) pick({ anchor: range.anchor, head })
    },
    onClick: (event) => {
      const index = lineOf(event.target)
      if (index < 0) return
      if (!inGutter(event.target)) {
        if (coarse) gutterAt(index)?.focus()
        return
      }
      const range = live.current
      pick(null)
      if (range) return comment(range)
      const growing = placed
        .filter((entry) => entry.comment.draft !== undefined && (event.shiftKey || (coarse && !entry.comment.body && !entry.comment.draft)))
        .at(-1)
      if (!growing) return comment({ anchor: index, head: index })
      const to = reach(file.lines, index < growing.start ? growing.start : growing.end, index)
      growComment(review.runID, growing.comment.id, {
        scope: review.scope,
        lines: file.lines.slice(Math.min(growing.start, to), Math.max(growing.end, to) + 1),
      })
    },
    onKeyDown: (event) => {
      if (!inGutter(event.target)) return
      if (event.key === 'Escape' && live.current) {
        event.preventDefault()
        pick(null)
      }
      if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
      event.preventDefault()
      const step = event.key === 'ArrowDown' ? 1 : -1
      const from = lineOf(event.target)
      let to = from + step
      if (event.shiftKey) {
        if (!commentable(file.lines[to])) return
        pick({ anchor: live.current?.anchor ?? from, head: to })
      } else {
        while (file.lines[to] && !commentable(file.lines[to])) to += step
        if (!file.lines[to]) return
        pick(null)
      }
      gutterAt(to)?.focus()
    },
    onFocus: (event) => {
      if (inGutter(event.target)) setCursor(lineOf(event.target))
    },
    onBlur: (event) => {
      if (!event.currentTarget.contains(event.relatedTarget)) pick(null)
    },
  }

  return {
    body,
    handlers,
    threads,
    marked,
    outdated,
    picking: picked !== null,
    tab: commentable(file.lines[cursor]) ? cursor : file.lines.findIndex(commentable),
    focusLine: (index: number) => requestAnimationFrame(() => gutterAt(index)?.focus()),
  }
}

const Line = memo(function Line({
  line,
  index,
  wrap,
  gutter,
  review,
  marked,
  tab,
}: {
  line: PatchLine
  index: number
  wrap: boolean
  gutter?: string
  /** The gutter keeps room for the `+`, and is a button on a line that takes comments. */
  review: boolean
  marked: boolean
  tab: boolean
}) {
  const numbers = gutter && (
    <>
      <LineNumber value={line.old} width={gutter} marked={marked} />
      <LineNumber value={line.new} width={review ? `calc(${gutter} + 0.75rem)` : gutter} marked={marked} last className={cn(review && 'pr-5')} />
    </>
  )
  return (
    <div data-line={index} className={cn('group/line flex leading-5', !wrap && 'min-w-max', lineTint[line.kind])}>
      {review && commentable(line) ? (
        <LineGutter
          tabIndex={tab ? 0 : -1}
          aria-label={`Comment on ${line.new === undefined ? `removed line ${line.old}` : `line ${line.new}`}`}
        >
          {numbers}
        </LineGutter>
      ) : numbers}
      <code className={cn('min-w-0 flex-1 px-2', wrap ? 'break-all whitespace-pre-wrap' : 'whitespace-pre')}>
        {linePrefix[line.kind]}
        {line.text || ' '}
      </code>
    </div>
  )
})

function LineNumber({ value, width, last, marked, className }: { value?: number; width: string; last?: boolean; marked: boolean; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        'shrink-0 px-2 text-right tabular-nums select-none',
        marked ? 'bg-selection text-text' : 'text-muted',
        last && 'border-r border-seam',
        className,
      )}
      style={{ width }}
    >
      {value ?? ''}
    </span>
  )
}

function lastLine(file: PatchFile): number {
  let last = 0
  for (const line of file.lines) last = Math.max(last, line.old ?? 0, line.new ?? 0)
  return last
}
