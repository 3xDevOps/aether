import { useEffect, useRef } from 'react'
import { Check, File as FileIcon, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { StatusDot } from '@/components/ui/status-dot'
import { formatBytes } from '@/lib/format'
import { useTimedFlag } from '@/lib/hooks'
import { cn } from '@/lib/utils'
import { attachmentName, attachmentStates, isImage, usePreviewURL, type AttachmentState, type ComposerImageState } from '@/routes/run/composer-images'
import type { ComposerImage } from '@/routes/run/composer-state'

const checkHold = 1000

function ChipState({ state, attached }: { state: AttachmentState; attached: boolean }) {
  const [justAttached, flash] = useTimedFlag(checkHold)
  const was = useRef(attached)
  useEffect(() => {
    if (attached && !was.current) flash()
    was.current = attached
  }, [attached, flash])
  if (state.status === 'uploading') return <Spinner className="size-3" />
  if (state.status === 'failed' || state.status === 'idle') return <StatusDot tone="failed" label="Not uploaded" />
  if (justAttached) return <Check aria-hidden className="size-3 shrink-0 animate-pop-in text-state-done motion-reduce:animate-none" />
  return null
}

function Chip({ image, index, state, disabled, onRemove }: {
  image: ComposerImage
  index: number
  state: AttachmentState
  disabled: boolean
  onRemove: () => void
}) {
  const picture = isImage(image)
  const preview = usePreviewURL(image)
  const name = attachmentName(image)
  const waiting = state.status === 'waiting'
  const remove = (
    <Button
      variant="ghost"
      size="icon-sm"
      className={cn('size-5 coarse:size-5 after:absolute after:-inset-2 coarse:after:-inset-3', picture ? 'absolute -top-1.5 -right-1.5 rounded-full border border-seam bg-raised' : 'relative')}
      label={`Remove attached ${picture ? 'image' : 'file'} ${index + 1}: ${name}`}
      disabled={disabled}
      onClick={onRemove}
    >
      <X className="size-3" />
    </Button>
  )
  const mark = <ChipState state={state} attached={Boolean(image.path)} />
  if (picture) {
    return (
      <div className="relative size-10 shrink-0 rounded-control border border-seam bg-canvas" title={name}>
        {preview && <img src={preview} alt={name} className={cn('size-full rounded-control object-cover', waiting && 'opacity-muted')} />}
        <span className="absolute right-0.5 bottom-0.5 flex size-4 items-center justify-center rounded-full bg-canvas empty:hidden">{mark}</span>
        {remove}
      </div>
    )
  }
  return (
    <div className="flex h-10 max-w-56 shrink-0 items-center gap-2 rounded-control border border-seam bg-canvas pr-1 pl-2.5" title={name}>
      <FileIcon aria-hidden className="size-4 shrink-0 text-muted" />
      <div className="min-w-0">
        <p className={cn('truncate text-ui-sm leading-4', waiting && 'text-muted')}>{name}</p>
        <p className="text-ui-xs leading-4 text-muted">{formatBytes(image.file.size)}</p>
      </div>
      <span className="flex size-4 shrink-0 items-center justify-center">{mark}</span>
      {remove}
    </div>
  )
}

export function AttachmentChips({ images, disabled }: { images: ComposerImageState; disabled: boolean }) {
  if (!images.previews.length) return null
  const states = attachmentStates(images.previews, images.uploading, images.uploadError)
  return (
    <div role="group" className="flex max-h-32 flex-wrap gap-2 overflow-y-auto px-3 pt-3" aria-label="Attached files">
      {images.previews.map((image, index) => (
        <Chip
          key={image.id}
          image={image}
          index={index}
          state={states[index]}
          disabled={disabled || images.uploading}
          onRemove={() => images.remove(index)}
        />
      ))}
    </div>
  )
}
