import type * as React from 'react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { File as FileIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn, focusRingInset } from '@/lib/utils'
import { useStore } from '@/store'
import type { MessageImageRef } from '@/store/session-rows'

const maxScale = 4
const keyStep = 1.25

function ImageViewer({ url, name, gallery, zoomed, keys, refocus, onStep }: {
  url: string
  name: string
  gallery: boolean
  zoomed: React.RefObject<boolean>
  keys: React.RefObject<((event: React.KeyboardEvent) => void) | undefined>
  refocus: boolean
  onStep: (by: 1 | -1) => void
}) {
  // Pixels drawn per natural pixel; undefined while the image is fitted to the frame.
  const [scale, setScale] = useState<number>()
  const pending = useRef<number>(undefined)
  const [natural, setNatural] = useState<{ width: number; height: number }>()
  const [zoomable, setZoomable] = useState(false)
  const frame = useRef<HTMLDivElement>(null)
  const img = useRef<HTMLImageElement>(null)
  // The picture point (0 to 1) to keep under a frame point (pixels) after the scale changes.
  const anchor = useRef({ x: 0.5, y: 0.5, left: -1, top: -1 })
  const swipe = useRef<{ x: number; y: number }>(undefined)
  const pinch = useRef<{ distance: number; scale: number }>(undefined)

  const fitScale = () => {
    const el = frame.current
    if (!el || !natural) return 1
    return Math.min(1, el.clientWidth / natural.width, el.clientHeight / natural.height)
  }

  /** Scale to `next` (undefined fits), keeping the picture under the client point, or the frame's middle, still. */
  const zoomTo = (next: number | undefined, clientX?: number, clientY?: number) => {
    const el = frame.current
    const picture = img.current
    if (!el || !picture?.naturalWidth) return
    const box = el.getBoundingClientRect()
    const x = clientX ?? box.left + el.clientWidth / 2
    const y = clientY ?? box.top + el.clientHeight / 2
    // Where the point falls on the picture, 0 to 1 on each axis, measured on what is drawn since several wheel
    // events can land before the next render. A fitted img letterboxes its picture.
    const drawn = picture.getBoundingClientRect()
    const shown = picture.classList.contains('object-scale-down')
      ? Math.min(1, drawn.width / picture.naturalWidth, drawn.height / picture.naturalHeight)
      : drawn.width / picture.naturalWidth
    const width = picture.naturalWidth * shown
    const height = picture.naturalHeight * shown
    const clamp = (n: number) => (Number.isFinite(n) ? Math.min(1, Math.max(0, n)) : 0.5)
    anchor.current = {
      x: clamp((x - drawn.left - (drawn.width - width) / 2) / width),
      y: clamp((y - drawn.top - (drawn.height - height) / 2) / height),
      left: x - box.left,
      top: y - box.top,
    }
    pending.current = next === undefined || next <= fitScale() * 1.001 ? undefined : Math.min(maxScale, next)
    setScale(pending.current)
  }
  const zoomBy = (factor: number, clientX?: number, clientY?: number) => zoomTo((pending.current ?? fitScale()) * factor, clientX, clientY)
  const zoomRef = useRef(zoomBy)
  zoomRef.current = zoomBy

  useLayoutEffect(() => {
    zoomed.current = scale !== undefined
    const el = frame.current
    const picture = img.current
    if (!el || !picture) return
    // Scroll by how far the anchored point sits from where it belongs; the picture may be centred, not at 0.
    const { x, y, left, top } = anchor.current
    const box = el.getBoundingClientRect()
    const drawn = picture.getBoundingClientRect()
    el.scrollLeft += drawn.left + x * drawn.width - (box.left + (left < 0 ? el.clientWidth / 2 : left))
    el.scrollTop += drawn.top + y * drawn.height - (box.top + (top < 0 ? el.clientHeight / 2 : top))
  }, [scale, zoomed])

  // A trackpad pinch arrives as a ctrl wheel (Safari: gesture events); React's wheel listener is passive and cannot
  // keep the browser from zooming the whole page.
  useEffect(() => {
    const el = frame.current
    if (!el) return
    let gesture = 1
    const wheel = (event: WheelEvent) => {
      if (!event.ctrlKey) return
      event.preventDefault()
      // A mouse wheel notch reports about 100; a trackpad pinch many small deltas.
      zoomRef.current(Math.exp(-Math.max(-25, Math.min(25, event.deltaY)) * 0.01), event.clientX, event.clientY)
    }
    type Gesture = UIEvent & { scale: number; clientX: number; clientY: number }
    const start = (event: Event) => {
      event.preventDefault()
      gesture = 1
    }
    const change = (event: Event) => {
      const { scale: next, clientX, clientY } = event as Gesture
      event.preventDefault()
      // iOS Safari reports a touch pinch as touches and gestures both; the touch handlers zoom it.
      if (pinch.current) return
      zoomRef.current(next / gesture, clientX, clientY)
      gesture = next
    }
    el.addEventListener('wheel', wheel, { passive: false })
    el.addEventListener('gesturestart', start)
    el.addEventListener('gesturechange', change)
    return () => {
      el.removeEventListener('wheel', wheel)
      el.removeEventListener('gesturestart', start)
      el.removeEventListener('gesturechange', change)
    }
  }, [])

  // Whether the picture outgrows the frame changes as the window does.
  useEffect(() => {
    const el = frame.current
    if (!el || !natural) return
    const measure = () => setZoomable(natural.width > el.clientWidth || natural.height > el.clientHeight)
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [natural])

  // Stepping remounts the viewer, which removes the control that had focus.
  useEffect(() => {
    if (refocus) frame.current?.focus()
  }, [refocus])

  const image = (
    <img
      ref={img}
      src={url}
      alt={name}
      className={cn(
        scale === undefined ? 'size-full object-scale-down' : 'max-w-none shrink-0 cursor-zoom-out',
        scale === undefined && zoomable && 'cursor-zoom-in',
      )}
      style={scale === undefined || !natural ? undefined : { width: natural.width * scale, height: natural.height * scale }}
      onLoad={(event) => setNatural({ width: event.currentTarget.naturalWidth, height: event.currentTarget.naturalHeight })}
    />
  )

  const onKey = (event: React.KeyboardEvent) => {
    if (!(event.metaKey || event.ctrlKey) || event.altKey) return
    const { key, code } = event
    // A layout can put another character on these keys (AZERTY's 0 is à), so the key's place counts too; not the
    // keypad's, whose 0 with Num Lock off is Insert, and Mod+Insert copies.
    const by = key === '=' || key === '+' || code === 'Equal' ? keyStep
      : key === '-' || code === 'Minus' ? 1 / keyStep
      : key === '0' || code === 'Digit0' ? 0
      : undefined
    if (by === undefined) return
    event.preventDefault()
    if (by === 0) zoomTo(undefined)
    else zoomBy(by)
  }
  useLayoutEffect(() => {
    keys.current = onKey
  })

  return (
    <div
      className="contents"
      onTouchStart={(event) => {
        const [a, b] = [event.touches[0], event.touches[1]]
        pinch.current = event.touches.length === 2 && a && b
          ? { distance: Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY), scale: scale ?? fitScale() }
          : undefined
        swipe.current = event.touches.length === 1 && a ? { x: a.clientX, y: a.clientY } : undefined
      }}
      onTouchMove={(event) => {
        const [a, b] = [event.touches[0], event.touches[1]]
        const start = pinch.current
        if (!start || event.touches.length !== 2 || !a || !b) return
        const ratio = Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY) / start.distance
        zoomTo(start.scale * ratio, (a.clientX + b.clientX) / 2, (a.clientY + b.clientY) / 2)
      }}
      onTouchEnd={(event) => {
        if (event.touches.length < 2) pinch.current = undefined
        const begin = swipe.current
        const touch = event.changedTouches[0]
        swipe.current = undefined
        // A pinch-zoomed page pans under the finger; that drag is not a swipe.
        if (scale !== undefined || !begin || !touch || (window.visualViewport?.scale ?? 1) > 1) return
        const dx = touch.clientX - begin.x
        if (Math.abs(dx) > 48 && Math.abs(dx) > 2 * Math.abs(touch.clientY - begin.y)) onStep(dx < 0 ? 1 : -1)
      }}
    >
      <DialogHeader className="min-h-6 flex-row items-center justify-between gap-3 coarse:min-h-11 coarse:pr-12">
        <DialogTitle className="truncate">{name}</DialogTitle>
        <DialogDescription className="sr-only">
          {[
            'Pinch, or press Command or Control with plus, minus or zero, to zoom.',
            gallery && 'Use the arrow keys or swipe to see the other images.',
          ].filter(Boolean).join(' ')}
        </DialogDescription>
        <div className="flex items-center gap-2">
          {scale !== undefined && <span className="text-ui-sm text-muted tabular-nums">{Math.round(scale * 100)}%</span>}
          {zoomable && (
            <Button variant="ghost" size="sm" aria-pressed={scale === 1} className="aria-pressed:bg-accent-soft aria-pressed:text-accent" onClick={() => zoomTo(scale === 1 ? undefined : 1)}>
              Actual size
            </Button>
          )}
        </div>
      </DialogHeader>
      <div
        ref={frame}
        tabIndex={0}
        role="group"
        aria-label={`${name}, ${scale === undefined ? 'fitted' : `zoomed to ${Math.round(scale * 100)}%`}`}
        className={cn(
          'flex min-h-0 touch-pan-x touch-pan-y overflow-auto rounded-control border border-seam bg-canvas outline-none',
          'focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-focus',
        )}
        onClick={(event) => {
          // Only the picture: a click on the frame's own scrollbar must not reset the zoom.
          if (event.target !== img.current) return
          if (scale !== undefined) zoomTo(undefined)
          else if (zoomable) zoomTo(1, event.clientX, event.clientY)
        }}
      >
        <div className={scale === undefined ? 'size-full' : 'm-auto shrink-0'}>{image}</div>
      </div>
    </div>
  )
}

function MessageImage({ runID, messageID, index, name, onURL, onOpen }: MessageImageRef & {
  runID: string
  name: string
  onURL: (url: string | undefined) => void
  onOpen: () => void
}) {
  const [url, setURL] = useState<string>()
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const report = useRef(onURL)
  report.current = onURL
  useEffect(() => {
    const abort = new AbortController()
    let objectURL: string | undefined
    void api.roomImage(runID, messageID, index, abort.signal).then((blob) => {
      if (abort.signal.aborted) return
      objectURL = URL.createObjectURL(blob)
      setURL(objectURL)
      report.current(objectURL)
    }).catch((cause) => {
      if (!abort.signal.aborted) setError(message(cause))
    })
    return () => {
      abort.abort()
      if (!objectURL) return
      report.current(undefined)
      URL.revokeObjectURL(objectURL)
    }
  }, [runID, messageID, index])

  return (
    <div className="relative flex h-48 w-full max-w-80 items-center justify-center overflow-hidden rounded-control border border-seam bg-canvas transition-colors has-[button:hover]:border-control">
      {error ? (
        <p role="alert" className="p-3 text-ui-sm break-words text-state-failed">{name} could not be loaded: {error}</p>
      ) : (
        <>
          {!loaded && <span role="status" className="absolute p-3 text-ui-sm text-muted">Loading {name.toLowerCase()}…</span>}
          {url && (
            <Button type="button" variant="quiet" aria-label={`Open ${name.toLowerCase()} full size`} className={cn('size-full cursor-zoom-in coarse:h-full', focusRingInset)} onClick={onOpen}>
              <img
                src={url}
                alt={name}
                className="size-full object-contain"
                onLoad={() => setLoaded(true)}
                onError={() => {
                  report.current(undefined)
                  setError('The downloaded image could not be decoded.')
                }}
              />
            </Button>
          )}
        </>
      )}
    </div>
  )
}

export function MessageAttachments({ runID, images = [], files = [] }: { runID: string; images?: MessageImageRef[]; files?: string[] }) {
  const identityKey = useStore((s) => s.identityKey)
  const cacheEpoch = useStore((s) => s.terminalCacheEpoch)
  const [urls, setURLs] = useState<Record<string, string>>({})
  const [openID, setOpenID] = useState<string>()
  const list = useRef<HTMLDivElement>(null)
  const lastShown = useRef<string>(undefined)
  const zoomed = useRef(false)
  const keys = useRef<((event: React.KeyboardEvent) => void) | undefined>(undefined)
  const refocus = useRef(false)
  const show = (id: string, keepFocus = false) => {
    refocus.current = keepFocus
    lastShown.current = id
    setOpenID(id)
  }
  const ids = images.map((image) => `${image.messageID}:${image.index}`)
  const name = (position: number) => (images.length > 1 ? `Image ${position + 1} of ${images.length}` : 'Attached image')
  const shown = openID === undefined ? -1 : ids.indexOf(openID)
  const url = openID === undefined ? undefined : urls[openID]
  const viewable = ids.filter((id) => urls[id])
  const step = (by: 1 | -1) => {
    const next = ids.slice(by > 0 ? shown + 1 : 0, by > 0 ? undefined : shown).filter((id) => urls[id])
    const id = by > 0 ? next[0] : next.at(-1)
    if (id) show(id, document.activeElement?.closest('[data-slot=dialog-content]') != null)
  }

  return (
    <div ref={list} className="mt-2 flex flex-wrap items-start gap-2" aria-label="Attachments">
      {images.map((image, position) => {
        const id = ids[position]!
        return (
          <div key={JSON.stringify([identityKey, cacheEpoch, runID, image.messageID, image.index])} data-image={id} className="contents">
            <MessageImage
              runID={runID}
              name={name(position)}
              onURL={(next) => {
                setURLs(({ [id]: _, ...rest }) => (next ? { ...rest, [id]: next } : rest))
                if (!next) setOpenID((open) => (open === id ? undefined : open))
              }}
              onOpen={() => show(id)}
              {...image}
            />
          </div>
        )
      })}
      {files.map((path) => (
        <div key={path} title={path} className="flex max-w-80 items-center gap-2 rounded-control border border-seam bg-canvas px-2 py-1">
          <FileIcon className="shrink-0 text-muted" />
          <span className="truncate text-ui-sm">{path.slice(path.lastIndexOf('/') + 1)}</span>
        </div>
      ))}
      <Dialog open={url !== undefined} onOpenChange={(open) => !open && setOpenID(undefined)}>
        <DialogContent
          className="inset-4 h-auto max-h-none w-auto max-w-none translate-x-0 translate-y-0 grid-rows-[auto_minmax(0,1fr)] overflow-hidden max-md:inset-0 max-md:h-dvh max-md:max-h-none max-md:rounded-none max-md:border-0 max-md:pt-[calc(1rem+var(--safe-top))] max-md:[&>button:last-child]:top-[calc(0.5rem+var(--safe-top))]"
          onKeyDown={(event) => {
            keys.current?.(event)
            if (event.defaultPrevented) return
            // Zoomed in, the arrows scroll the picture instead.
            if (zoomed.current || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
            if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
            event.preventDefault()
            step(event.key === 'ArrowRight' ? 1 : -1)
          }}
          onCloseAutoFocus={(event) => {
            // Return to the thumbnail of the image last shown, not the one first opened.
            const thumbnail = Array.from(list.current?.querySelectorAll<HTMLElement>('[data-image]') ?? [])
              .find((el) => el.dataset.image === lastShown.current)?.querySelector('button')
            if (!thumbnail) return
            event.preventDefault()
            thumbnail.focus()
          }}
        >
          {url && openID && (
            <ImageViewer key={openID} url={url} name={name(shown)} gallery={viewable.length > 1} zoomed={zoomed} keys={keys} refocus={refocus.current} onStep={step} />
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
