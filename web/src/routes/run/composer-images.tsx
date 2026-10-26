import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { File as FileIcon, Plus, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api, isTerminalImageType } from '@/lib/api'
import { formatBytes, message } from '@/lib/format'
import { clipboardFiles } from '@/lib/term-clipboard'
import { beginDraftRequest, composerDraft, saveComposerDraft, useComposerDraft, type ComposerImage } from '@/routes/run/composer-state'

export const maxAttachments = 8

/** Images go to the agent as images; every other file goes as a file path. */
export function isImage(attachment: ComposerImage): boolean {
  return isTerminalImageType(attachment.file.type)
}

export function hasImages(previews: ComposerImage[]): boolean {
  return previews.some(isImage)
}

let nextID = 0

export interface ComposerImageState {
  attachments: string[]
  previews: ComposerImage[]
  uploading: boolean
  ready: boolean
  uploadError: string | undefined
  upload: (files: File[]) => Promise<void>
  retry: () => Promise<void>
  remove: (index: number) => void
  onPaste: (event: React.ClipboardEvent<HTMLTextAreaElement>) => void
  clearError: () => void
  isUploading: () => boolean
  isReady: () => boolean
  getPaths: () => string[]
}

function pathsOf(images: ComposerImage[]): string[] {
  return images.flatMap((image) => image.path ? [image.path] : [])
}

export function useComposerImages(runID: string, enabled: boolean): ComposerImageState {
  const { images: previews, uploading, uploadError } = useComposerDraft(runID)

  const remove = (index: number) => {
    const draft = composerDraft(runID)
    if (draft.busy || draft.uploading) return
    saveComposerDraft(runID, { images: draft.images.filter((_, at) => at !== index), uploadError: undefined })
  }
  const retry = async () => {
    const draft = composerDraft(runID)
    if (!enabled || draft.busy || draft.uploading) return
    const request = beginDraftRequest(runID, 'uploading', { uploadError: undefined })
    try {
      for (;;) {
        const image = request.current() ? composerDraft(runID).images.find((item) => !item.path) : undefined
        if (!image) break
        const { path } = await (isImage(image) ? api.uploadTerminalImage : api.uploadTerminalFile)(image.file, runID)
        if (request.current()) {
          saveComposerDraft(runID, { images: composerDraft(runID).images.map((item) => item.id === image.id ? { ...item, path } : item) })
        }
      }
      request.settle()
    } catch (cause) {
      request.settle({ uploadError: `Upload failed: ${message(cause)}` })
    }
  }
  const upload = async (files: File[]) => {
    const draft = composerDraft(runID)
    if (!files.length || !enabled || draft.busy || draft.uploading) return
    if (draft.images.length + files.length > maxAttachments) {
      saveComposerDraft(runID, { uploadError: `Attach at most ${maxAttachments} files.` })
      return
    }
    saveComposerDraft(runID, { images: [...draft.images, ...files.map((file) => ({ id: nextID++, file }))] })
    await retry()
  }

  const onPaste = (event: React.ClipboardEvent<HTMLTextAreaElement>) => {
    if (!enabled) return
    const files = clipboardFiles(event.clipboardData)
    if (!files.length) return
    if (!event.clipboardData.getData('text/plain')) event.preventDefault()
    void upload(files)
  }

  return {
    attachments: pathsOf(previews),
    previews, uploading: Boolean(uploading), ready: !uploading && previews.every((image) => Boolean(image.path)),
    uploadError, upload, retry, remove, onPaste,
    clearError: () => saveComposerDraft(runID, { uploadError: undefined }),
    isUploading: () => Boolean(composerDraft(runID).uploading),
    isReady: () => !composerDraft(runID).uploading && composerDraft(runID).images.every((image) => Boolean(image.path)),
    getPaths: () => pathsOf(composerDraft(runID).images),
  }
}

export function ComposerImagePicker({ images, disabled, unsupported }: {
  images: ComposerImageState
  disabled: boolean
  unsupported?: string
}) {
  const picker = useRef<HTMLInputElement>(null)
  return (
    <>
      <input
        ref={picker}
        type="file"
        multiple
        className="sr-only"
        tabIndex={-1}
        aria-label="Choose files to attach"
        disabled={disabled || images.uploading || images.previews.length >= maxAttachments}
        onChange={(event) => {
          const files = Array.from(event.currentTarget.files ?? [])
          event.currentTarget.value = ''
          void images.upload(files)
        }}
      />
      <Button
        variant="ghost"
        size="icon-sm"
        label={images.previews.length >= maxAttachments ? `At most ${maxAttachments} files` : 'Attach files'}
        disabled={disabled || images.uploading || images.previews.length >= maxAttachments}
        onClick={() => picker.current?.click()}
      >
        <Plus />
      </Button>
      {unsupported && <span className="text-ui-sm text-muted">{unsupported}</span>}
      {!images.ready && !images.uploading && (
        <Button size="sm" variant="ghost" disabled={disabled} onClick={() => void images.retry()}>
          Retry upload
        </Button>
      )}
    </>
  )
}

function ComposerImagePreview({ image, index, disabled, onRemove }: {
  image: ComposerImage
  index: number
  disabled: boolean
  onRemove: () => void
}) {
  const picture = isImage(image)
  const [preview, setPreview] = useState<string>()
  useEffect(() => {
    if (!picture || typeof URL.createObjectURL !== 'function') return
    const url = URL.createObjectURL(image.file)
    setPreview(url)
    return () => URL.revokeObjectURL(url)
  }, [image.file, picture])
  const name = image.file.name || (picture ? 'Clipboard image' : 'Clipboard file')
  const remove = (
    <Button
      className={picture ? 'absolute right-0 top-0 bg-canvas' : undefined}
      variant="ghost"
      size="icon-sm"
      label={`Remove attached ${picture ? 'image' : 'file'} ${index + 1}: ${name}`}
      disabled={disabled}
      onClick={onRemove}
    >
      <X />
    </Button>
  )
  if (!picture) {
    return (
      <div className="flex max-w-64 shrink-0 items-center gap-2 self-start rounded-control border border-seam bg-canvas py-1 pl-2">
        <FileIcon className="shrink-0 text-muted" />
        <div className="min-w-0">
          <p className="truncate text-ui-sm" title={name}>{name}</p>
          <p className="text-ui-sm text-muted">{formatBytes(image.file.size)}{!image.path && ' · Not uploaded'}</p>
        </div>
        {remove}
      </div>
    )
  }
  return (
    <div className="relative w-28 shrink-0 rounded-control border border-seam bg-canvas p-1">
      {preview && <img src={preview} alt={name} className="h-20 w-full rounded-chip object-contain" />}
      <p className="truncate px-1 pt-1 text-ui-sm text-muted" title={name}>{name}</p>
      {!image.path && <p className="px-1 text-ui-sm text-muted">Not uploaded</p>}
      {remove}
    </div>
  )
}

export function ComposerImages({ images, disabled }: {
  images: ComposerImageState
  disabled: boolean
}) {
  if (!images.previews.length) return null
  return (
    <div className="flex max-h-64 flex-wrap gap-2 overflow-y-auto px-3 pt-3" aria-label="Attached files">
      {images.previews.map((image, index) => (
        <ComposerImagePreview
          key={image.id}
          image={image}
          index={index}
          disabled={disabled || images.uploading}
          onRemove={() => images.remove(index)}
        />
      ))}
    </div>
  )
}
