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

/** Where a point falls on the picture an object-fit box draws, from 0 to 1 on each axis. */
function pointOn(img: HTMLImageElement | null, clientX: number, clientY: number) {
  if (!img?.naturalWidth || !img.naturalHeight) return undefined
  const box = img.getBoundingClientRect()
  const scale = Math.min(1, box.width / img.naturalWidth, box.height / img.naturalHeight)
  const width = img.naturalWidth * scale
  const height = img.naturalHeight * scale
  const clamp = (n: number) => Math.min(1, Math.max(0, n))
  return {
    x: clamp((clientX - box.left - (box.width - width) / 2) / width),
    y: clamp((clientY - box.top - (box.height - height) / 2) / height),
  }
}

function ImageViewer({ url, name, gallery, zoomed, refocus, onStep }: {
  url: string
  name: string
  gallery: boolean
  zoomed: React.RefObject<boolean>
  refocus: boolean
  onStep: (by: 1 | -1) => void
}) {
  const [actual, setActual] = useState(false)
  const [zoomable, setZoomable] = useState(false)
  const frame = useRef<HTMLDivElement>(null)
  const zoom = useRef<HTMLButtonElement>(null)
  const focus = useRef({ x: 0.5, y: 0.5 })
  const swipe = useRef<{ x: number; y: number }>(undefined)

  useLayoutEffect(() => {
    zoomed.current = actual
    const el = frame.current
    if (!el) return
    el.scrollLeft = focus.current.x * el.scrollWidth - el.clientWidth / 2
    el.scrollTop = focus.current.y * el.scrollHeight - el.clientHeight / 2
  }, [actual, zoomed])

  // Stepping remounts the viewer, which removes the control that had focus.
  useEffect(() => {
    if (refocus) zoom.current?.focus()
  }, [refocus, zoomable])

  const toggle = (point = { x: 0.5, y: 0.5 }) => {
    focus.current = point
    setActual((on) => !on)
  }

  const image = (
    <img
      src={url}
      alt={name}
      className={actual ? 'max-w-none' : 'size-full object-scale-down'}
      onLoad={(event) => {
        const img = event.currentTarget
        const el = frame.current
        if (el) setZoomable(img.naturalWidth > el.clientWidth || img.naturalHeight > el.clientHeight)
      }}
    />
  )

  return (
    <div
      className="contents"
      onTouchStart={(event) => {
        const touch = event.touches[0]
        swipe.current = event.touches.length === 1 && touch ? { x: touch.clientX, y: touch.clientY } : undefined
      }}
      onTouchEnd={(event) => {
        const start = swipe.current
        const touch = event.changedTouches[0]
        swipe.current = undefined
        // A pinch-zoomed page pans under the finger; that drag is not a swipe.
        if (actual || !start || !touch || (window.visualViewport?.scale ?? 1) > 1) return
        const dx = touch.clientX - start.x
        if (Math.abs(dx) > 48 && Math.abs(dx) > 2 * Math.abs(touch.clientY - start.y)) onStep(dx < 0 ? 1 : -1)
      }}
    >
      <DialogHeader className="min-h-6 flex-row items-center justify-between gap-3 coarse:min-h-11 coarse:pr-12">
        <DialogTitle className="truncate">{name}</DialogTitle>
        <DialogDescription className="sr-only">
          {[
            zoomable ? 'Switch between fitting the window and actual size.' : 'Shown at actual size.',
            gallery && 'Use the arrow keys or swipe to see the other images.',
          ].filter(Boolean).join(' ')}
        </DialogDescription>
        {zoomable && (
          <Button variant="ghost" size="sm" aria-pressed={actual} className="aria-pressed:bg-accent-soft aria-pressed:text-accent" onClick={() => toggle()}>
            Actual size
          </Button>
        )}
      </DialogHeader>
      <div
        ref={frame}
        className={cn(
          'flex min-h-0 overflow-auto rounded-control border border-seam bg-canvas',
          'has-[:focus-visible]:outline-2 has-[:focus-visible]:-outline-offset-2 has-[:focus-visible]:outline-focus',
        )}
      >
        {zoomable ? (
          <Button
            ref={zoom}
            type="button"
            variant="quiet"
            aria-label={actual ? 'Fit image to window' : 'Show image at actual size'}
            className={cn('rounded-none outline-none', actual ? 'm-auto cursor-zoom-out' : 'size-full cursor-zoom-in')}
            onClick={(event) => {
              // Enter and Space report no pointer position; zoom on the middle then.
              toggle(event.detail === 0 ? undefined : pointOn(event.currentTarget.querySelector('img'), event.clientX, event.clientY))
            }}
          >
            {image}
          </Button>
        ) : (
          <div className="size-full">{image}</div>
        )}
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
            <Button type="button" variant="quiet" aria-label={`Open ${name.toLowerCase()} full size`} className={cn('size-full cursor-zoom-in', focusRingInset)} onClick={onOpen}>
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
            <ImageViewer key={openID} url={url} name={name(shown)} gallery={viewable.length > 1} zoomed={zoomed} refocus={refocus.current} onStep={step} />
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
