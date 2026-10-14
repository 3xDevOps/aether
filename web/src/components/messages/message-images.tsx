import { useEffect, useState } from 'react'
import { File as FileIcon } from '@/components/icons'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'
import type { MessageImageRef } from '@/store/session-rows'

function MessageImage({ runID, messageID, index }: MessageImageRef & { runID: string }) {
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
    <div className="relative flex h-48 w-full max-w-80 items-center justify-center overflow-hidden rounded-control border border-seam bg-canvas">
      {error ? (
        <p role="alert" className="p-3 text-ui-sm break-words text-state-failed">Image {index + 1} could not be loaded: {error}</p>
      ) : (
        <>
          {!loaded && <span role="status" className="absolute p-3 text-ui-sm text-muted">Loading image {index + 1}…</span>}
          {url && (
            <img
              src={url}
              alt={`Attached image ${index + 1}`}
              className="size-full object-contain"
              onLoad={() => setLoaded(true)}
              onError={() => setError('The downloaded image could not be decoded.')}
            />
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
