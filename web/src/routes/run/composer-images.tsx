import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { Paperclip, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api, TERMINAL_IMAGE_TYPES } from '@/lib/api'
import { message } from '@/lib/format'
import { imageFiles } from '@/lib/term-clipboard'

export const maxAttachments = 8
export const imageTypes = TERMINAL_IMAGE_TYPES.join(',')

interface ComposerImage {
  id: number
  file: File
  path?: string
}

export interface ComposerImageState {
  attachments: string[]
  previews: ComposerImage[]
  uploading: boolean
  ready: boolean
  uploadError: string | undefined
  upload: (files: File[]) => Promise<void>
  retry: () => Promise<void>
  clear: () => void
  remove: (index: number) => void
  onPaste: (event: React.ClipboardEvent<HTMLTextAreaElement>) => void
  clearError: () => void
  isUploading: () => boolean
  isReady: () => boolean
  getPaths: () => string[]
}

export function useComposerImages(runID: string, enabled: boolean, isBusy: () => boolean): ComposerImageState {
  const [previews, setPreviews] = useState<ComposerImage[]>([])
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState<string>()
  const selected = useRef<ComposerImage[]>([])
  const paths = useRef<string[]>([])
  const nextID = useRef(0)
  const pending = useRef(false)
  const generation = useRef(0)
  const target = useRef({ runID, enabled, isBusy })
  target.current = { runID, enabled, isBusy }

  useEffect(() => {
    selected.current = []
    paths.current = []
    pending.current = false
    setPreviews([])
    setUploading(false)
    setUploadError(undefined)
    return () => { generation.current += 1 }
  }, [runID])

  const update = (next: ComposerImage[]) => {
    selected.current = next
    paths.current = next.flatMap((image) => image.path ? [image.path] : [])
    setPreviews(next)
  }
  const clear = () => {
    generation.current += 1
    pending.current = false
    setUploading(false)
    setUploadError(undefined)
    update([])
  }
  const remove = (index: number) => {
    if (target.current.isBusy() || pending.current) return
    update(selected.current.filter((_, at) => at !== index))
    setUploadError(undefined)
  }
  const retry = async () => {
    if (!target.current.enabled || target.current.isBusy() || pending.current) return
    const token = generation.current
    const current = () => token === generation.current && target.current.runID === runID
    pending.current = true
    setUploading(true)
    setUploadError(undefined)
    try {
      for (const image of selected.current) {
        if (image.path) continue
        if (!current() || !target.current.enabled) return
        const result = await api.uploadTerminalImage(image.file, runID)
        if (!current() || !target.current.enabled) return
        update(selected.current.map((item) => item.id === image.id ? { ...item, path: result.path } : item))
      }
    } catch (cause) {
      if (current()) setUploadError(`Image upload failed: ${message(cause)}`)
    } finally {
      if (current()) {
        pending.current = false
        setUploading(false)
      }
    }
  }
  const upload = async (files: File[]) => {
    if (!files.length || !target.current.enabled || target.current.isBusy() || pending.current) return
    if (selected.current.length + files.length > maxAttachments) {
      setUploadError(`Attach at most ${maxAttachments} images.`)
      return
    }
    update([...selected.current, ...files.map((file) => ({ id: nextID.current++, file }))])
    await retry()
  }

  const onPaste = (event: React.ClipboardEvent<HTMLTextAreaElement>) => {
    if (!target.current.enabled) return
    const files = imageFiles(event.clipboardData)
    if (!files.length) return
    if (!event.clipboardData.getData('text/plain')) event.preventDefault()
    void upload(files)
  }

  return {
    attachments: previews.flatMap((image) => image.path ? [image.path] : []),
    previews, uploading, ready: !uploading && previews.every((image) => Boolean(image.path)),
    uploadError, upload, retry, clear, remove, onPaste,
    clearError: () => setUploadError(undefined),
    isUploading: () => pending.current,
    isReady: () => !pending.current && selected.current.every((image) => Boolean(image.path)),
    getPaths: () => paths.current,
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
        accept={imageTypes}
        multiple
        className="sr-only"
        tabIndex={-1}
        aria-label="Choose images to attach"
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
        label={unsupported ?? (images.previews.length >= maxAttachments ? `At most ${maxAttachments} images` : 'Attach an image')}
        disabled={disabled || images.uploading || images.previews.length >= maxAttachments}
        onClick={() => picker.current?.click()}
      >
        <Paperclip />
      </Button>
      {unsupported && <span className="text-ui-sm text-muted">{unsupported}</span>}
      {!images.ready && !images.uploading && (
        <Button size="sm" variant="ghost" disabled={disabled} onClick={() => void images.retry()}>
          Retry image upload
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
  const [preview, setPreview] = useState<string>()
  useEffect(() => {
    if (typeof URL.createObjectURL !== 'function') return
    const url = URL.createObjectURL(image.file)
    setPreview(url)
    return () => URL.revokeObjectURL(url)
  }, [image.file])
  const name = image.file.name || 'Clipboard image'
  return (
    <div className="relative w-28 shrink-0 rounded-control border border-seam bg-canvas p-1">
      {preview && <img src={preview} alt={name} className="h-20 w-full rounded-[2px] object-contain" />}
      <p className="truncate px-1 pt-1 text-ui-sm text-muted" title={name}>{name}</p>
      {!image.path && <p className="px-1 text-ui-sm text-muted">Not uploaded</p>}
      <Button
        className="absolute right-0 top-0 bg-canvas"
        variant="ghost"
        size="icon-sm"
        label={`Remove attached image ${index + 1}: ${name}`}
        disabled={disabled}
        onClick={onRemove}
      >
        <X />
      </Button>
    </div>
  )
}

export function ComposerImages({ images, disabled }: {
  images: ComposerImageState
  disabled: boolean
}) {
  if (!images.previews.length) return null
  return (
    <div className="flex max-h-64 flex-wrap gap-2 overflow-y-auto p-2" aria-label="Attached images">
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
