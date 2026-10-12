import { useEffect, useImperativeHandle, useRef, useState } from 'react'
import type { CSSProperties, Ref, RefObject } from 'react'
import { Camera } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { coarseHitArea } from '@/components/ui/checkbox'
import { api, ApiError } from '@/lib/api'
import { message } from '@/lib/format'
import { useElementSize } from '@/lib/hooks'
import type { DevBrowserElement, DevBrowserFrameMetadata, DevBrowserPage } from '@/lib/types'
import { cn } from '@/lib/utils'
import { addNote, elementLabel, feedback, maxNotes, removeNote, updateNote, useFeedback, type ElementNote, type Note } from './feedback'
import { cropFrame, frameBox, type FrameRect } from './frames'
import { NoteCard } from './note-card'
import type { BrowserPick, BrowserSurfaceHandle } from './surface'

const methodNotFound = -32601
const hoverInterval = 100
const cardWidth = 320
/** The height a card is given room for when choosing the side of its element. */
const cardRoom = 200

interface Hover { element: DevBrowserElement; frame: DevBrowserFrameMetadata }
interface Size { width: number; height: number }
interface Pin {
  note: Note
  number: number
  /** The element's box, drawn while its card is open. */
  box: FrameRect | null
  label: string
  at: CSSProperties
}

function sameGeometry(a: DevBrowserFrameMetadata, b: DevBrowserFrameMetadata): boolean {
  return a.page_revision === b.page_revision && a.viewport_id === b.viewport_id && a.width === b.width && a.height === b.height
    && (a.scroll_x ?? 0) === (b.scroll_x ?? 0) && (a.scroll_y ?? 0) === (b.scroll_y ?? 0)
    && (a.offset_top ?? 0) === (b.offset_top ?? 0) && (a.page_scale_factor ?? 1) === (b.page_scale_factor ?? 1)
}

/** Whether the page was read where the frame showed it: a scroll in between puts another element under the point. */
function scrolledSince(element: DevBrowserElement, shown: DevBrowserFrameMetadata): boolean {
  return Math.abs(element.scroll_x - (shown.scroll_x ?? 0)) > 2 || Math.abs(element.scroll_y - (shown.scroll_y ?? 0)) > 2
}

/** `button#save 120 × 36`, the way an element inspector labels its box. */
function boxLabel(element: DevBrowserElement): string {
  const id = element.attributes?.id
  const first = element.attributes?.class?.split(/\s+/).find(Boolean)
  return `${element.tag}${id ? `#${id}` : first ? `.${first}` : ''} ${Math.round(element.bounds.width)} × ${Math.round(element.bounds.height)}`
}

// Two rings, so the box shows on a page of any colour.
const outline = 'absolute outline-2 outline-accent ring-3 ring-canvas/70'

function boxStyle(box: FrameRect): CSSProperties {
  return { left: box.left, top: box.top, width: box.width, height: box.height }
}

/**
 * The annotation layer over the page: the box under the pointer, a numbered
 * pin for each note on this page, and the card of the note being read or
 * written. It draws beside the canvas and never into the page.
 */
export function AnnotateLayer({ ref, runID, page, surface, picking, onUnavailable, onNotice, onCaptures }: {
  ref: Ref<BrowserPick>
  runID: string
  page: DevBrowserPage
  surface: RefObject<BrowserSurfaceHandle | null>
  /** False when the pointer drives the page: notes are shown, none is added. */
  picking: boolean
  /** The companion cannot inspect; `reason` is the server's own. */
  onUnavailable: (reason: string) => void
  onNotice: (text: string) => void
  onCaptures: (opener: HTMLElement | null) => void
}) {
  const { notes } = useFeedback(runID)
  const [sizeRef, size] = useElementSize<HTMLDivElement>()
  const [frame, setFrame] = useState<DevBrowserFrameMetadata | null>(null)
  const [hover, setHover] = useState<Hover | null>(null)
  const [viewing, setViewing] = useState('')
  const wanted = useRef<{ x: number; y: number } | null>(null)
  const reading = useRef(false)
  const alive = useRef(true)
  const latest = useRef({ page, onUnavailable, onNotice })
  latest.current = { page, onUnavailable, onNotice }

  const inspect = (point: { x: number; y: number }, shown: DevBrowserFrameMetadata) => api.devBrowserInspect({
    run_id: runID, session_id: shown.session_id, page_id: shown.page_id, page_revision: shown.page_revision, viewport_id: shown.viewport_id, ...point,
  })
  const unavailable = (cause: unknown) => {
    if (!(cause instanceof ApiError) || cause.code !== methodNotFound) return false
    latest.current.onUnavailable(message(cause))
    return true
  }

  // One read at a time, each for where the pointer is now: the box can trail
  // the pointer by a frame, and a queue would only make it trail further.
  const follow = async () => {
    if (reading.current) return
    reading.current = true
    try {
      while (alive.current && wanted.current) {
        const point = wanted.current
        wanted.current = null
        const shown = surface.current?.frame()
        if (!shown) return
        const started = performance.now()
        try {
          const result = await inspect(point, shown)
          if (alive.current) setHover(result.element && !scrolledSince(result.element, shown) ? { element: result.element, frame: shown } : null)
        } catch (cause) {
          // A frame that moved on answers stale; the next move reads again.
          if (!alive.current || unavailable(cause)) return
          setHover(null)
        }
        await new Promise((resolve) => setTimeout(resolve, Math.max(0, hoverInterval - (performance.now() - started))))
      }
    } finally { reading.current = false }
  }

  const select = (point: { x: number; y: number }) => {
    const shown = surface.current?.frame()
    if (!shown) return
    if (feedback(runID).notes.length >= maxNotes) {
      latest.current.onNotice(`One message carries at most ${maxNotes} notes. Send these, then add more.`)
      return
    }
    const id = crypto.randomUUID()
    // The pixels the person pointed at: a later frame may show something else.
    const painted = surface.current?.snapshot() ?? null
    wanted.current = null
    setHover(null)
    setViewing('')
    addNote(runID, {
      kind: 'element', id, body: '', point,
      page: { url: latest.current.page.url, width: shown.width, height: shown.height },
      at: { page_id: shown.page_id, scroll_x: shown.scroll_x ?? 0, scroll_y: shown.scroll_y ?? 0 },
    })
    void (async () => {
      try {
        const { element } = await inspect(point, shown)
        if (!element) updateNote(runID, id, { failure: 'The page has no element at that point.' })
        else if (scrolledSince(element, shown)) updateNote(runID, id, { failure: 'The page scrolled before it was read. Delete this note and click the element again.' })
        else updateNote(runID, id, { element, image: (painted && await cropFrame(painted, shown, element.bounds)) ?? undefined })
      } catch (cause) {
        if (unavailable(cause)) removeNote(runID, id)
        else updateNote(runID, id, { failure: message(cause) })
      }
    })()
  }

  useImperativeHandle(ref, () => ({
    hover: (point) => {
      wanted.current = point
      if (point) void follow()
      else setHover(null)
    },
    select,
    frame: (next) => setFrame((previous) => (previous && sameGeometry(previous, next) ? previous : next)),
  }))
  useEffect(() => {
    alive.current = true
    setFrame(surface.current?.frame() ?? null)
    return () => { alive.current = false }
  }, [surface])
  useEffect(() => {
    if (!picking) setHover(null)
  }, [picking])

  return (
    <div ref={sizeRef} className="pointer-events-none absolute inset-0 overflow-hidden">
      {frame && size.width > 0 && (
        <Marks runID={runID} page={page} frame={frame} size={size} notes={notes} hover={hover} viewing={viewing} onView={setViewing} onCaptures={onCaptures} />
      )}
    </div>
  )
}

function Marks({ runID, page, frame, size, notes, hover, viewing, onView, onCaptures }: {
  runID: string
  page: DevBrowserPage
  frame: DevBrowserFrameMetadata
  size: Size
  notes: Note[]
  hover: Hover | null
  viewing: string
  onView: (id: string) => void
  onCaptures: (opener: HTMLElement | null) => void
}) {
  const open = notes.find((note) => note.draft !== undefined) ?? notes.find((note) => note.id === viewing)
  /** Where a note's element is painted now, or null when this frame does not show its page as it was. */
  const place = (note: ElementNote): FrameRect | null => {
    if (note.at.page_id !== page.page_id || note.page.url !== page.url || note.page.width !== frame.width || note.page.height !== frame.height) return null
    const bounds = note.element?.bounds ?? { ...note.point, width: 0, height: 0 }
    return frameBox(frame, size, {
      ...bounds,
      x: bounds.x + note.at.scroll_x - (frame.scroll_x ?? 0),
      y: bounds.y + note.at.scroll_y - (frame.scroll_y ?? 0),
    })
  }
  let shots = 0
  const pins = notes.flatMap((note, at): Pin[] => {
    if (note.kind === 'screenshot') return [{ note, number: at + 1, box: null, label: 'screenshot of the page', at: { right: 8 + shots++ * 26, top: 8 } }]
    const box = place(note)
    if (!box) return []
    return [{
      note, number: at + 1, box: note.element ? box : null,
      label: note.element ? elementLabel(note.element) : 'reading the element',
      at: { left: Math.max(2, Math.min(box.left - 10, size.width - 22)), top: Math.max(2, Math.min(box.top - 10, size.height - 22)) },
    }]
  })
  const hoverBox = hover && !open && sameGeometry(hover.frame, frame) ? frameBox(frame, size, hover.element.bounds) : null
  // A screenshot is of the whole page, so its card hangs from the corner its pin is in.
  const anchor = open && ((open.kind === 'element' && place(open)) || { left: size.width, top: 8, width: 0, height: 20 })
  const width = Math.min(cardWidth, size.width - 16)
  const below = anchor ? anchor.top + anchor.height + 8 : 0
  const card: CSSProperties | undefined = anchor ? {
    width,
    left: Math.max(8, Math.min(anchor.left, size.width - width - 8)),
    ...(below + cardRoom <= size.height ? { top: Math.max(8, below) }
      : anchor.top - 8 >= cardRoom ? { bottom: size.height - anchor.top + 8 }
        : { bottom: 8 }),
  } : undefined

  return (
    <>
      {hover && hoverBox && (
        <>
          <div aria-hidden className={cn(outline, 'bg-accent/10')} style={boxStyle(hoverBox)} />
          <span
            aria-hidden
            data-slot="annotate-hover"
            className="absolute max-w-full truncate rounded-control bg-accent px-1 font-code text-ui-xs text-on-accent"
            style={{ left: Math.max(0, hoverBox.left), top: hoverBox.top >= 20 ? hoverBox.top - 20 : Math.max(0, hoverBox.top + hoverBox.height + 2) }}
          >
            {boxLabel(hover.element)}
          </span>
        </>
      )}
      {pins.map(({ note, number, box, label, at }) => {
        const chosen = open?.id === note.id
        return (
          <div key={note.id}>
            {box && chosen && <div aria-hidden className={outline} style={boxStyle(box)} />}
            <Button
              size="icon-sm"
              label={`Note ${number}: ${label}`}
              aria-expanded={chosen}
              // A marker stays its own size under a finger; the hit area around it does the reaching.
              className={cn(coarseHitArea, 'pointer-events-auto absolute size-5 rounded-full text-ui-xs tabular-nums shadow-overlay coarse:size-5')}
              style={at}
              onClick={() => {
                if (note.draft === undefined) onView(chosen ? '' : note.id)
              }}
            >
              {note.kind === 'screenshot' ? <Camera className="size-3" /> : number}
            </Button>
          </div>
        )
      })}
      {open && card && (
        <div className="pointer-events-auto absolute" style={card}>
          <NoteCard runID={runID} note={open} index={notes.indexOf(open) + 1} onClose={() => onView('')} onCaptures={onCaptures} />
        </div>
      )}
    </>
  )
}
