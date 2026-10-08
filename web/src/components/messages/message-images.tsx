import { useEffect, useState } from 'react'
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

export function MessageImages({ runID, images }: { runID: string; images: MessageImageRef[] }) {
  const identityKey = useStore((s) => s.identityKey)
  const cacheEpoch = useStore((s) => s.terminalCacheEpoch)
  return (
    <div className="mt-2 flex flex-wrap gap-2" aria-label="Attached images">
      {images.map((image) => (
        <MessageImage key={JSON.stringify([identityKey, cacheEpoch, runID, image.messageID, image.index])} runID={runID} {...image} />
      ))}
    </div>
  )
}
