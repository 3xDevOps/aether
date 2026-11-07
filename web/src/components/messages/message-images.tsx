import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { File as FileIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
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

function ImageViewer({ url, name }: { url: string; name: string }) {
  const [actual, setActual] = useState(false)
  const [zoomable, setZoomable] = useState(false)
  const frame = useRef<HTMLDivElement>(null)
  const focus = useRef({ x: 0.5, y: 0.5 })

  useLayoutEffect(() => {
    const el = frame.current
    if (!el) return
    el.scrollLeft = focus.current.x * el.scrollWidth - el.clientWidth / 2
    el.scrollTop = focus.current.y * el.scrollHeight - el.clientHeight / 2
  }, [actual])

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
    <>
      <DialogHeader className="min-h-6 flex-row items-center justify-between gap-3 coarse:min-h-11 coarse:pr-12">
        <DialogTitle className="truncate">{name}</DialogTitle>
        <DialogDescription className="sr-only">
          {zoomable ? 'Switch between fitting the window and actual size.' : 'Shown at actual size.'}
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
          <button
            type="button"
            aria-label={actual ? 'Fit image to window' : 'Show image at actual size'}
            className={cn('outline-none', actual ? 'm-auto cursor-zoom-out' : 'size-full cursor-zoom-in')}
            onClick={(event) => {
              // Enter and Space report no pointer position; zoom on the middle then.
              toggle(event.detail === 0 ? undefined : pointOn(event.currentTarget.querySelector('img'), event.clientX, event.clientY))
            }}
          >
            {image}
          </button>
        ) : (
          <div className="size-full">{image}</div>
        )}
      </div>
    </>
  )
}

function MessageImage({ runID, messageID, index, name }: MessageImageRef & { runID: string; name: string }) {
  const [url, setURL] = useState<string>()
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  useEffect(() => {
    const abort = new AbortController()
    let objectURL: string | undefined
    void api.roomImage(runID, messageID, index, abort.signal).then((blob) => {
      if (abort.signal.aborted) return
      objectURL = URL.createObjectURL(blob)
      setURL(objectURL)
    }).catch((cause) => {
      if (!abort.signal.aborted) setError(message(cause))
    })
    return () => {
      abort.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
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
            <Dialog>
              <DialogTrigger asChild>
                <button type="button" aria-label={`Open ${name.toLowerCase()} full size`} className={cn('size-full cursor-zoom-in', focusRingInset)}>
                  <img
                    src={url}
                    alt={name}
                    className="size-full object-contain"
                    onLoad={() => setLoaded(true)}
                    onError={() => setError('The downloaded image could not be decoded.')}
                  />
                </button>
              </DialogTrigger>
              <DialogContent className="inset-4 h-auto max-h-none w-auto max-w-none translate-x-0 translate-y-0 grid-rows-[auto_minmax(0,1fr)] overflow-hidden max-md:inset-0 max-md:h-dvh max-md:max-h-none max-md:rounded-none max-md:border-0 max-md:pt-[calc(1rem+var(--safe-top))]">
                <ImageViewer url={url} name={name} />
              </DialogContent>
            </Dialog>
          )}
        </>
      )}
    </div>
  )
}

export function MessageAttachments({ runID, images = [], files = [] }: { runID: string; images?: MessageImageRef[]; files?: string[] }) {
  const identityKey = useStore((s) => s.identityKey)
  const cacheEpoch = useStore((s) => s.terminalCacheEpoch)
  return (
    <div className="mt-2 flex flex-wrap items-start gap-2" aria-label="Attachments">
      {images.map((image, position) => (
        <MessageImage
          key={JSON.stringify([identityKey, cacheEpoch, runID, image.messageID, image.index])}
          runID={runID}
          name={images.length > 1 ? `Image ${position + 1} of ${images.length}` : 'Attached image'}
          {...image}
        />
      ))}
      {files.map((path) => (
        <div key={path} title={path} className="flex max-w-80 items-center gap-2 rounded-control border border-seam bg-canvas px-2 py-1">
          <FileIcon className="shrink-0 text-muted" />
          <span className="truncate text-ui-sm">{path.slice(path.lastIndexOf('/') + 1)}</span>
        </div>
      ))}
    </div>
  )
}
