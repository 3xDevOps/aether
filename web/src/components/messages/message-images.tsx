import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { File as FileIcon } from '@/components/icons'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { cn, focusRingInset } from '@/lib/utils'
import { useStore } from '@/store'
import type { MessageImageRef } from '@/store/session-rows'

function ZoomableImage({ url, alt }: { url: string; alt: string }) {
  const [actual, setActual] = useState(false)
  const area = useRef<HTMLDivElement>(null)
  const focus = useRef({ x: 0.5, y: 0.5 })

  useLayoutEffect(() => {
    const el = area.current
    if (!el) return
    el.scrollLeft = focus.current.x * el.scrollWidth - el.clientWidth / 2
    el.scrollTop = focus.current.y * el.scrollHeight - el.clientHeight / 2
  }, [actual])

  return (
    <div ref={area} className="flex max-h-[calc(85dvh-9rem)] overflow-auto rounded-control border border-seam bg-canvas md:max-h-[calc(100dvh-10rem)]">
      <button
        type="button"
        aria-label={actual ? 'Fit image to window' : 'Show image at actual size'}
        className={cn('m-auto block', focusRingInset, actual ? 'cursor-zoom-out' : 'cursor-zoom-in')}
        onClick={(event) => {
          const box = event.currentTarget.getBoundingClientRect()
          // Enter and Space report no pointer position; zoom on the middle then.
          focus.current = event.detail === 0
            ? { x: 0.5, y: 0.5 }
            : { x: (event.clientX - box.left) / box.width, y: (event.clientY - box.top) / box.height }
          setActual((on) => !on)
        }}
      >
        <img src={url} alt={alt} className={actual ? 'max-w-none' : 'max-h-[calc(85dvh-9rem)] max-w-full object-contain md:max-h-[calc(100dvh-10rem)]'} />
      </button>
    </div>
  )
}

function MessageImage({ runID, messageID, index }: MessageImageRef & { runID: string }) {
  const [url, setURL] = useState<string>()
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState<string>()
  const alt = `Attached image ${index + 1}`
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
    <div className="relative flex h-48 w-full max-w-80 items-center justify-center overflow-hidden rounded-control border border-seam bg-canvas">
      {error ? (
        <p role="alert" className="p-3 text-ui-sm break-words text-state-failed">Image {index + 1} could not be loaded: {error}</p>
      ) : (
        <>
          {!loaded && <span role="status" className="absolute p-3 text-ui-sm text-muted">Loading image {index + 1}…</span>}
          {url && (
            <Dialog>
              <DialogTrigger asChild>
                <button type="button" aria-label={`View image ${index + 1} full size`} className={cn('size-full cursor-zoom-in', focusRingInset)}>
                  <img
                    src={url}
                    alt={alt}
                    className="size-full object-contain"
                    onLoad={() => setLoaded(true)}
                    onError={() => setError('The downloaded image could not be decoded.')}
                  />
                </button>
              </DialogTrigger>
              <DialogContent className="w-max min-w-[min(24rem,calc(100%-2rem))] max-w-[calc(100%-2rem)]">
                <DialogHeader>
                  <DialogTitle>{alt}</DialogTitle>
                  <DialogDescription>Click the image to switch between fit and actual size.</DialogDescription>
                </DialogHeader>
                <ZoomableImage url={url} alt={alt} />
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
      {images.map((image) => (
        <MessageImage key={JSON.stringify([identityKey, cacheEpoch, runID, image.messageID, image.index])} runID={runID} {...image} />
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
