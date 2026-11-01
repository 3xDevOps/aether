import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type * as React from 'react'
import { Virtualizer, type VirtualizerHandle } from 'virtua'
import { ArrowDown } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover'
import { Skeleton } from '@/components/ui/skeleton'
import { virtualizeAbove } from '@/components/workflow/workflow-tree'
import { useIsMobile } from '@/lib/breakpoints'
import { clockTime } from '@/lib/format'
import { useElementSize } from '@/lib/hooks'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import type { RunNavigation } from '@/routes/run/header'
import { stackedInset, trackGap, TraceAxisHeader, TraceGrid, TraceNowMarker, TraceStatsStrip, treeWidth } from '@/routes/run/trace-chrome'
import { useTraceData } from '@/routes/run/trace-data'
import { TraceDetail } from '@/routes/run/trace-detail'
import { TraceClock, TraceRowItem, type TraceGeometry } from '@/routes/run/trace-row'
import { useTraceAnnouncement } from '@/routes/run/trace-announce'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'
import { loadOlderItems } from '@/store/session-stream'
import { findSpan, parentIDs, pathOf } from '@/store/trace'
import { axisAt, axisMarks, withHeadroom } from '@/store/trace-axis'
import { emptyTraceView, type TraceView as TraceViewState } from '@/store/trace-log'
import { flattenTrace, type TraceRow } from '@/store/trace-rows'

/** Below this width the rows stack: the name line, then the bar on its own track. */
export const stackBelow = 561

const zoomPad = 0.05
const labelRoom = 40
const freshAtMost = 8
const bottomSlack = 4
const modeName = { tui: 'Standard', acp: 'Enhanced' }

export function TraceView({ run, nav }: { run: RunRecord; nav: RunNavigation }) {
  const data = useTraceData(run)
  const { root, stats, axis, now, nowA, live, session } = data
  const mobile = useIsMobile()
  const ui = useStore((s) => s.traceViews[run.id]) ?? emptyTraceView
  const setView = useCallback((patch: Partial<TraceViewState>) => useStore.getState().setTraceView(run.id, patch), [run.id])
  const [frameRef, frame] = useElementSize<HTMLDivElement>()
  const [frameNode, setFrameNode] = useState<HTMLDivElement | null>(null)
  const frameRefs = useCallback((node: HTMLDivElement | null) => {
    frameRef(node)
    setFrameNode(node)
  }, [frameRef])
  const [listRef, list] = useElementSize<HTMLDivElement>()
  const wide = frame.width === 0 || frame.width >= stackBelow
  const track = Math.max(0, (list.width || frame.width) - (wide ? treeWidth + trackGap : 2 * stackedInset))

  const zoomSpan = ui.zoom ? findSpan(root, ui.zoom) : undefined
  let origin = 0
  let span = axis.length
  if (zoomSpan) {
    const from = axisAt(axis, zoomSpan.start)
    // A running span's window steps through the headroom ladder, so the zoom holds still between steps.
    const to = zoomSpan.end === null ? from + withHeadroom(nowA - from) : axisAt(axis, zoomSpan.end)
    const pad = Math.max((to - from) * zoomPad, 500)
    origin = Math.max(0, from - pad)
    span = to + pad - origin
  }
  const scale = track > 0 ? track / span : 0
  // The axis glides only once a measured scale is on screen; otherwise every bar would glide in from the left on open.
  const [glide, setGlide] = useState(false)
  useLayoutEffect(() => {
    if (glide || scale === 0 || !frameNode) return
    getComputedStyle(frameNode).getPropertyValue('--trace-scale')
    setGlide(true)
  }, [glide, scale, frameNode])
  const geometry = useMemo<TraceGeometry>(() => ({ axis, origin, scale, track, wide }), [axis, origin, scale, track, wide])
  // The last label needs room before the track's end.
  const marks = useMemo(() => (scale > 0 ? axisMarks(axis, origin, origin + span - labelRoom / scale, scale) : []), [axis, origin, span, scale])

  const rows = useMemo(() => flattenTrace(root, ui.open), [root, ui.open])
  const active = rows.some((row) => row.key === ui.active) ? ui.active! : rows.at(-1)?.key
  const [selected, setSelected] = useState<string | null>(null)
  const selectedRow = rows.find((row) => row.key === selected)
  useEffect(() => {
    if (selected && !selectedRow) setSelected(null)
  }, [selected, selectedRow])
  const slowest = stats.slowest
  const announcement = useTraceAnnouncement(root)

  const scroller = useRef<HTMLDivElement | null>(null)
  const virtualizer = useRef<VirtualizerHandle>(null)
  const virtual = rows.length > virtualizeAbove
  const pinned = useRef(true)
  const [following, setFollowing] = useState(true)
  const elements = useRef(new Map<string, HTMLElement>())
  const pending = useRef<string | null>(null)

  const seen = useRef<Set<string> | null>(null)
  seen.current ??= new Set(rows.map((row) => row.key))
  const unseen = rows.filter((row) => !seen.current!.has(row.key)).length
  useEffect(() => {
    for (const row of rows) seen.current!.add(row.key)
  }, [rows])

  const focusKey = useCallback((key: string, element: HTMLElement) => {
    if (pending.current !== key) return
    pending.current = null
    const current = document.activeElement
    if (!current || current === document.body || scroller.current?.contains(current)) element.focus()
  }, [])
  const register = useCallback((key: string, element: HTMLElement | null) => {
    if (!element) {
      elements.current.delete(key)
      return
    }
    elements.current.set(key, element)
    focusKey(key, element)
  }, [focusKey])
  useEffect(() => {
    const key = pending.current
    const element = key ? elements.current.get(key) : undefined
    if (key && element) focusKey(key, element)
  })

  const toBottom = () => {
    const node = scroller.current
    if (node) node.scrollTop = node.scrollHeight
  }
  // Follow new rows at the end, but never scroll for a fold the person made.
  const folded = useRef(false)
  const lastKey = rows.at(-1)?.key
  useLayoutEffect(() => {
    if (pinned.current && !folded.current) toBottom()
  }, [lastKey, frame.height])
  useLayoutEffect(() => {
    folded.current = false
  }, [rows])

  const reveal = useCallback((key: string) => {
    const index = rows.findIndex((row) => row.key === key)
    if (index === -1) return
    if (virtual) virtualizer.current?.scrollToIndex(index, { align: 'nearest' })
    else elements.current.get(key)?.scrollIntoView?.({ block: 'nearest' })
  }, [rows, virtual])

  const focusRow = useCallback((key: string | undefined) => {
    if (!key) return
    pending.current = key
    setView({ active: key })
    reveal(key)
    const element = elements.current.get(key)
    if (element) focusKey(key, element)
  }, [setView, reveal, focusKey])

  const setOpen = useCallback((id: string, open: boolean) => {
    folded.current = true
    setView({ open: { ...useStore.getState().traceViews[run.id]?.open, [id]: open } })
  }, [setView, run.id])
  const onToggle = useCallback((row: TraceRow) => {
    setView({ active: row.key })
    if (row.expanded !== undefined) setOpen(row.span.id, !row.expanded)
  }, [setView, setOpen])
  const onActivate = useCallback((row: TraceRow) => {
    setView({ active: row.key })
    setSelected((current) => (current === row.key ? null : row.key))
  }, [setView])

  const errorCursor = useRef(-1)
  const revealing = useRef<string | null>(null)
  useLayoutEffect(() => {
    const key = revealing.current
    if (!key) return
    revealing.current = null
    reveal(key)
  }, [reveal])
  const nextError = () => {
    const errors = stats.errors
    if (errors.length === 0) return
    errorCursor.current = (errorCursor.current + 1) % errors.length
    const id = errors[errorCursor.current]!
    const open = { ...ui.open }
    for (const ancestor of pathOf(root, id)?.slice(0, -1) ?? []) open[ancestor] = true
    pinned.current = false
    setFollowing(false)
    setView({ open, active: id })
    setSelected(id)
    // The Errors button keeps focus otherwise: a pending focus never takes it from outside the tree.
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
    pending.current = id
    revealing.current = id
  }

  const zoomTo = (id: string) => {
    setSelected(null)
    setView({ zoom: id })
  }

  const onKeyDown = (event: React.KeyboardEvent) => {
    const target = event.target as HTMLElement
    if (event.key === 'Escape') {
      if (selected) setSelected(null)
      else if (ui.zoom) setView({ zoom: undefined })
      else return
      event.preventDefault()
      return
    }
    if (target.getAttribute('role') !== 'treeitem') return
    const index = rows.findIndex((row) => row.key === target.dataset.key)
    const row = rows[index]
    if (!row) return
    switch (event.key) {
      case 'ArrowDown':
        focusRow(rows[index + 1]?.key)
        break
      case 'ArrowUp':
        focusRow(rows[index - 1]?.key)
        break
      case 'Home':
        focusRow(rows[0]?.key)
        break
      case 'End':
        focusRow(rows.at(-1)?.key)
        break
      case 'ArrowRight':
        if (row.expanded === false) setOpen(row.span.id, true)
        else if (row.expanded) focusRow(rows[index + 1]?.key)
        break
      case 'ArrowLeft':
        if (row.expanded) setOpen(row.span.id, false)
        else if (row.parentKey) focusRow(row.parentKey)
        break
      case 'Enter':
        if (event.shiftKey) {
          if (row.span.kind !== 'run') zoomTo(row.span.id)
        } else onActivate(row)
        break
      case ' ':
        onActivate(row)
        break
      default:
        return
    }
    event.preventDefault()
  }

  const closeDetail = () => setSelected(null)
  const detail = selectedRow && (
    <TraceDetail
      span={selectedRow.span}
      runID={run.id}
      runStart={root.start}
      onZoom={() => zoomTo(selectedRow.span.id)}
      onClose={closeDetail}
      onSession={() => nav.go('session')}
      onRequest={nav.focusRequest}
      onRun={(id) => useStore.getState().navigate('run', { runId: id, view: 'trace' })}
    />
  )
  const anchor = useMemo(() => ({
    current: {
      getBoundingClientRect: () => {
        const element = selected ? elements.current.get(selected) : undefined
        const bar = element?.querySelector('[data-bar]') ?? element
        const rect = (bar ?? document.body).getBoundingClientRect()
        const row = element?.getBoundingClientRect() ?? rect
        return new DOMRect(rect.left, row.top, 0, row.height)
      },
    },
  }), [selected])

  const item = (row: TraceRow) => (
    <TraceRowItem
      key={row.key}
      row={row}
      geometry={geometry}
      active={row.key === active}
      selected={row.key === selected}
      slowest={row.span.id === slowest}
      fresh={unseen <= freshAtMost && !seen.current!.has(row.key)}
      onActivate={onActivate}
      onToggle={onToggle}
      register={register}
    />
  )
  const activeIndex = rows.findIndex((row) => row.key === active)
  const selectedIndex = rows.findIndex((row) => row.key === selected)
  const keep = [activeIndex, selectedIndex].filter((index) => index >= 0)
  const connecting = !session || (session.stream === 'connecting' && session.turns.length === 0)
  const noTurns = !connecting && root.children.length === 0

  return (
    <TraceClock value={{ now, nowA }}>
      <div
        ref={frameRefs}
        className={cn('relative flex h-full min-h-0 flex-col bg-canvas', glide && 'trace-axis')}
        style={{ '--trace-origin': origin, '--trace-scale': scale, '--trace-now': nowA } as React.CSSProperties}
      >
        <TraceStatsStrip
          root={root}
          stats={stats}
          cost={data.cost}
          costError={data.log?.error}
          costCapped={Boolean(data.log?.capped)}
          partial={Boolean(session?.more || session?.truncatedBefore)}
          live={live}
          now={now}
          wide={wide}
          onErrors={nextError}
        />
        <HistoryNotice run={run} paged={data.paged} />
        <div className="relative flex min-h-0 flex-1 flex-col">
          <TraceAxisHeader
            marks={marks}
            wide={wide}
            track={track}
            zoomed={Boolean(zoomSpan)}
            onWholeRun={() => setView({ zoom: undefined })}
            onExpand={(open) => {
              folded.current = true
              setView({ open: Object.fromEntries(parentIDs(root).map((id) => [id, open || id === root.id])) })
            }}
          />
          <div className="relative min-h-0 flex-1">
            <TraceGrid marks={marks} axis={axis} wide={wide} track={track} live={live} />
            {wide && <span aria-hidden className="pointer-events-none absolute inset-y-0 w-px bg-seam" style={{ left: treeWidth }} />}
            <div
              ref={(node) => {
                scroller.current = node
                listRef(node)
              }}
              className="absolute inset-0 overflow-y-auto overscroll-contain"
              onScroll={(event) => {
                const node = event.currentTarget
                pinned.current = node.scrollTop + node.clientHeight >= node.scrollHeight - bottomSlack
                setFollowing(pinned.current)
              }}
            >
              <div role="tree" aria-label="Trace" onKeyDown={onKeyDown} className="pt-1 pb-4.5">
                {virtual ? (
                  <Virtualizer ref={virtualizer} scrollRef={scroller} keepMounted={keep}>
                    {rows.map(item)}
                  </Virtualizer>
                ) : (
                  rows.map(item)
                )}
                {connecting && Array.from({ length: 6 }, (_, i) => (
                  <div key={i} className="flex h-7.5 items-center px-4">
                    <Skeleton className="h-3.5 w-full" />
                  </div>
                ))}
                {noTurns && <p className="px-4 py-2 text-ui-sm text-muted">The agent has not started a turn yet.</p>}
                {run.switching && <p role="status" className="px-4 py-2 text-ui-sm text-muted">Switching to {modeName[run.switching]}…</p>}
              </div>
            </div>
          </div>
          {live && <TraceNowMarker wide={wide} track={track} />}
          {live && !following && (
            <Button
              variant="secondary"
              size="sm"
              className="absolute right-4 bottom-4 z-20"
              onClick={() => {
                pinned.current = true
                setFollowing(true)
                setView({ zoom: undefined })
                toBottom()
              }}
            >
              <ArrowDown />
              Jump to now
            </Button>
          )}
        </div>
        {mobile ? (
          <Dialog open={Boolean(detail)} onOpenChange={(open) => !open && closeDetail()}>
            <DialogContent variant="bottom" showCloseButton={false} aria-describedby={undefined} className="grid-cols-[minmax(0,1fr)] p-4">
              <DialogTitle className="sr-only">{selectedRow ? `${selectedRow.span.label} details` : 'Span details'}</DialogTitle>
              {detail}
            </DialogContent>
          </Dialog>
        ) : (
          <Popover open={Boolean(detail)} onOpenChange={(open) => !open && closeDetail()}>
            <PopoverAnchor virtualRef={anchor} />
            <PopoverContent
              side="bottom"
              align="start"
              sideOffset={0}
              aria-label={selectedRow ? `${selectedRow.span.label} details` : undefined}
              collisionBoundary={frameNode}
              className="w-[min(322px,calc(100vw-16px))]"
              onCloseAutoFocus={(event) => {
                event.preventDefault()
                if (selected) elements.current.get(selected)?.focus()
              }}
              onInteractOutside={(event) => {
                if (event.target instanceof Element && event.target.closest('[role=treeitem]')) event.preventDefault()
              }}
            >
              {detail}
            </PopoverContent>
          </Popover>
        )}
        <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">{announcement}</span>
      </div>
    </TraceClock>
  )
}

function HistoryNotice({ run, paged }: { run: RunRecord; paged: boolean }) {
  const session = useStore((s) => s.acpSessions[run.id])
  if (!session) return null
  const first = session.turns[0]?.items[0]?.time
  const load = () => void loadOlderItems(useStore, api, run.id)
  return (
    <>
      {session.olderError && (
        <Callout tone="failed" title="Earlier items could not be read" className="m-2" actions={<Button size="sm" variant="secondary" onClick={load}>Retry</Button>}>
          {session.olderError}
        </Callout>
      )}
      {session.more && !session.olderError && (
        <div role="status" className="flex h-8 shrink-0 items-center gap-2 border-b border-seam px-4 text-ui-sm text-muted">
          {session.olderLoading ? 'Loading earlier history…' : 'Earlier history not loaded'}
          {paged && !session.olderLoading && <Button variant="link" className="text-ui-sm" onClick={load}>Load earlier</Button>}
        </div>
      )}
      {session.truncatedBefore && first && (
        <p role="status" className={cn('flex h-8 shrink-0 items-center border-b border-seam px-4 text-ui-sm text-muted')}>
          History before {clockTime(first)} expired on the server.
        </p>
      )}
    </>
  )
}
