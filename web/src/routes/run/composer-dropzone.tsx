import { useRef, useState } from 'react'
import type * as React from 'react'
import { Plus } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { DropIllustration, DropZone } from '@/components/ui/drop-zone'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Spinner } from '@/components/ui/spinner'
import { fitLabel } from '@/lib/file-drag'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { AttachmentList } from '@/routes/run/attachment-rows'
import { limitsHint, maxAttachments, type ComposerImageState } from '@/routes/run/composer-images'

export function dropRoom(images: ComposerImageState, disabled: boolean): { limit: number; blocked?: string } {
  if (disabled) return { limit: 0, blocked: 'Files cannot be attached now' }
  if (images.uploading) return { limit: 0, blocked: 'Uploading…' }
  const room = maxAttachments - images.previews.length
  return room > 0 ? { limit: room } : { limit: 0, blocked: `At most ${maxAttachments} files` }
}

export function DropOverlay({ count, refused, blocked, limit }: { count: number; refused: boolean; blocked?: string; limit: number }) {
  const label = blocked ?? (refused ? fitLabel(limit) : `Drop to attach ${count} ${count === 1 ? 'file' : 'files'}`)
  return (
    <div
      data-slot="drop-overlay"
      data-drag={blocked || refused ? undefined : ''}
      className="group/zone pointer-events-none absolute inset-0 z-10 flex animate-scrim-in items-center justify-center overflow-hidden rounded-composer border border-dashed border-control bg-chrome data-drag:border-accent motion-reduce:animate-none"
    >
      <span aria-hidden className="absolute inset-0 hidden bg-accent-soft group-data-drag/zone:block" />
      <span className="relative flex flex-col items-center gap-2 text-ui font-medium">
        <DropIllustration />
        <span role="status">{label}</span>
      </span>
    </div>
  )
}

export function AttachButton({ images, disabled, unsupported, textarea }: {
  images: ComposerImageState
  disabled: boolean
  unsupported?: string
  textarea?: React.RefObject<HTMLTextAreaElement | null>
}) {
  const coarse = useMediaQuery(coarsePointer)
  const picker = useRef<HTMLInputElement>(null)
  const [open, setOpen] = useState(false)
  const room = dropRoom(images, disabled)
  const browse = () => picker.current?.click()
  const icon = images.uploading ? <Spinner className="size-4 text-text" /> : <Plus />
  return (
    <>
      <input
        ref={picker}
        type="file"
        multiple
        className="sr-only"
        tabIndex={-1}
        aria-label="Choose files to attach"
        disabled={Boolean(room.blocked)}
        onChange={(event) => {
          const files = Array.from(event.currentTarget.files ?? [])
          event.currentTarget.value = ''
          void images.upload(files)
        }}
      />
      {coarse ? (
        <Button variant="ghost" size="icon-sm" label="Attach files" disabled={Boolean(room.blocked)} onClick={browse}>{icon}</Button>
      ) : (
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger asChild>
            <Button variant="ghost" size="icon-sm" label="Attach files" disabled={disabled}>{icon}</Button>
          </PopoverTrigger>
          <PopoverContent
            side="top"
            aria-label="Attach files"
            className="overflow-x-hidden overflow-y-auto p-0"
            onCloseAutoFocus={(event) => {
              const active = document.activeElement
              if (images.previews.length === 0 || !textarea?.current || (active && active !== document.body)) return
              event.preventDefault()
              textarea.current.focus()
            }}
          >
            <div className="p-2">
              <DropZone onBrowse={browse} onFiles={(files) => void images.upload(files)} limit={room.limit} blocked={room.blocked} hint={limitsHint} />
            </div>
            <AttachmentList images={images} noImages={Boolean(unsupported)} locked={images.uploading} />
          </PopoverContent>
        </Popover>
      )}
      {unsupported && <span className="text-ui-sm text-muted">{unsupported}</span>}
      {!images.ready && !images.uploading && (
        <Button size="sm" variant="ghost" disabled={disabled} onClick={() => void images.retry()}>
          Retry upload
        </Button>
      )}
    </>
  )
}
