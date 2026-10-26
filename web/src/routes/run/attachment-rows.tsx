import { useState } from 'react'
import { File as FileIcon, RotateCw, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { ProgressBar } from '@/components/ui/progress-bar'
import { StatusDot } from '@/components/ui/status-dot'
import { TextSwap } from '@/components/ui/text-swap'
import { formatBytes } from '@/lib/format'
import { cn } from '@/lib/utils'
import { attachmentName, attachmentStates, isImage, usePreviewURL, type AttachmentState, type ComposerImageState } from '@/routes/run/composer-images'
import type { ComposerImage } from '@/routes/run/composer-state'

const staggerCap = 8
const labels = { attached: 'Attached', uploading: 'Uploading…', waiting: 'Waiting', idle: 'Not uploaded' }

function Row({ image, state, fresh, order, noImages, locked, onRetry, onRemove }: {
  image: ComposerImage
  state: AttachmentState
  fresh: boolean
  order: number
  noImages: boolean
  locked: boolean
  onRetry: () => void
  onRemove: () => void
}) {
  const name = attachmentName(image)
  const preview = usePreviewURL(image)
  const failed = state.status === 'failed'
  // Only a change seen while mounted is confirmed with motion; reopening the popover replays nothing.
  const [seen, setSeen] = useState(state.status)
  const [changed, setChanged] = useState(false)
  if (seen !== state.status) {
    setSeen(state.status)
    setChanged(true)
  }
  const refused = noImages && isImage(image)
  const status = refused ? 'Agent takes no images' : state.status === 'failed' ? state.reason : `${formatBytes(image.file.size)} · ${labels[state.status]}`
  return (
    <li
      className={cn('flex items-center gap-3 border-b border-seam px-3 py-2 last:border-b-0', fresh && 'animate-entrance motion-reduce:animate-none')}
      style={fresh ? { animationDelay: `${Math.min(order, staggerCap) * 30}ms` } : undefined}
    >
      {preview
        ? <img src={preview} alt="" className="size-8 shrink-0 rounded-control border border-seam object-cover" />
        : <span aria-hidden className="flex size-8 shrink-0 items-center justify-center rounded-control border border-seam bg-chrome text-muted"><FileIcon className="size-4" /></span>}
      <div className={cn('relative min-w-0 flex-1', failed && changed && 'animate-shake motion-reduce:animate-none')}>
        <div className="flex items-center gap-1.5">
          <span className="truncate text-ui font-medium" title={name}>{name}</span>
          {state.status === 'attached' && !refused && <StatusDot tone="done" label="Attached" className={cn(changed && 'animate-pop-in motion-reduce:animate-none')} />}
        </div>
        <TextSwap swapKey={`${state.status}${refused}`} className={`max-w-full text-ui-sm ${failed || refused ? 'text-state-failed' : 'text-muted'}`} layerClassName="truncate">
          {status}
        </TextSwap>
        {state.status === 'uploading' && <ProgressBar role="progressbar" aria-label={`Uploading ${name}`} className="absolute inset-x-0 -bottom-1 h-0.5 rounded-none" />}
      </div>
      {failed && (
        <Button variant="ghost" size="icon-sm" label={`Retry ${name}`} onClick={onRetry}>
          <RotateCw />
        </Button>
      )}
      <Button variant="ghost" size="icon-sm" label={`Remove ${name}`} disabled={locked} onClick={onRemove}>
        <X />
      </Button>
    </li>
  )
}

function Footer({ images, noImages }: { images: ComposerImageState; noImages: boolean }) {
  const { previews, uploading } = images
  const attached = previews.filter((image) => image.path)
  const kind = previews.length === 0 ? 'empty' : uploading ? 'busy' : noImages ? 'refused' : attached.length === previews.length ? 'done' : 'partial'
  const bytes = (list: ComposerImage[]) => list.reduce((sum, image) => sum + image.file.size, 0)
  const text = {
    empty: <span className="text-muted">No files yet</span>,
    refused: <span className="text-state-failed">This agent does not support image prompts.</span>,
    busy: <span>Uploading {attached.length + 1} of {previews.length} <span className="text-icon-faint">·</span> {formatBytes(bytes(attached))} of {formatBytes(bytes(previews))}</span>,
    done: <span className="flex items-center gap-2"><StatusDot tone="done" />{previews.length === 1 ? '1 file attached' : `${previews.length} files attached`}</span>,
    partial: <span className="flex items-center gap-2"><StatusDot tone="needs-you" />{attached.length} of {previews.length} attached</span>,
  }[kind]
  return (
    <div className="flex min-h-10 items-center justify-between gap-2 border-t border-seam bg-chrome py-1 pr-2 pl-3 text-ui-sm">
      <TextSwap swapKey={kind} mode="crossfade" layerClassName="whitespace-nowrap" className="min-w-0">
        <span role="status">{text}</span>
      </TextSwap>
      {!uploading && previews.length > 0 && (
        <span className="flex shrink-0 gap-1">
          {attached.length < previews.length && <Button size="sm" variant="secondary" onClick={() => void images.retry()}>Retry</Button>}
          <Button size="sm" variant="secondary" onClick={images.clear}>Clear</Button>
        </span>
      )}
    </div>
  )
}

export function AttachmentList({ images, noImages, locked }: { images: ComposerImageState; noImages: boolean; locked: boolean }) {
  const states = attachmentStates(images.previews, images.uploading, images.uploadError)
  const [initial] = useState(() => new Set(images.previews.map((image) => image.id)))
  let order = 0
  return (
    <>
      {images.previews.length > 0 && (
        <ul aria-label="Attachments" className="max-h-[40vh] overflow-y-auto">
          {images.previews.map((image, index) => (
            <Row
              key={image.id}
              image={image}
              state={states[index]}
              fresh={!initial.has(image.id)}
              order={initial.has(image.id) ? 0 : order++}
              noImages={noImages}
              locked={locked}
              onRetry={() => void images.retry()}
              onRemove={() => images.remove(index)}
            />
          ))}
        </ul>
      )}
      <Footer images={images} noImages={noImages} />
    </>
  )
}
