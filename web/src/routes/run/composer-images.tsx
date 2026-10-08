import { useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { Paperclip, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api, TERMINAL_IMAGE_TYPES } from '@/lib/api'
import { message } from '@/lib/format'
import { imageFiles } from '@/lib/term-clipboard'

export const maxAttachments = 8
export const imageTypes = TERMINAL_IMAGE_TYPES.join(',')

export interface ComposerImageState {
  attachments: string[]
  uploading: boolean
  uploadError: string | undefined
  upload: (files: File[]) => Promise<void>
  clear: () => void
  remove: (index: number) => void
  onPaste: (event: React.ClipboardEvent<HTMLTextAreaElement>) => void
  clearError: () => void
  isUploading: () => boolean
  getPaths: () => string[]
}

export function useComposerImages(runID: string, enabled: boolean, isBusy: () => boolean): ComposerImageState {
  const [attachments, setAttachments] = useState<string[]>([])
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState<string>()
  const paths = useRef<string[]>([])
  const pending = useRef(false)
  const generation = useRef(0)
  const target = useRef({ runID, enabled, isBusy })
  target.current = { runID, enabled, isBusy }

  useEffect(() => () => { generation.current += 1 }, [])

  const clear = () => {
    paths.current = []
    setAttachments([])
  }
  const remove = (index: number) => {
    if (target.current.isBusy()) return
    paths.current = paths.current.filter((_, at) => at !== index)
    setAttachments(paths.current)
  }
  const upload = async (files: File[]) => {
    if (!files.length || !target.current.enabled || target.current.isBusy() || pending.current) return
    if (paths.current.length + files.length > maxAttachments) {
      setUploadError(`Attach at most ${maxAttachments} images.`)
      return
    }
    const token = generation.current
    const current = () => token === generation.current && target.current.runID === runID
    pending.current = true
    setUploading(true)
    setUploadError(undefined)
    try {
      for (const file of files) {
        if (!current() || !target.current.enabled) return
        const result = await api.uploadTerminalImage(file, runID)
        if (!current() || !target.current.enabled) return
        paths.current = [...paths.current, result.path]
        setAttachments(paths.current)
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

  const onPaste = (event: React.ClipboardEvent<HTMLTextAreaElement>) => {
    if (!target.current.enabled) return
    const files = imageFiles(event.clipboardData)
    if (!files.length) return
    event.preventDefault()
    void upload(files)
  }

  return {
    attachments, uploading, uploadError, upload, clear, remove, onPaste,
    clearError: () => setUploadError(undefined),
    isUploading: () => pending.current,
    getPaths: () => paths.current,
  }
}

export function ComposerImages({ images, disabled }: {
  images: ComposerImageState
  disabled: boolean
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
        disabled={disabled || images.uploading || images.attachments.length >= maxAttachments}
        onChange={(event) => {
          const files = Array.from(event.currentTarget.files ?? [])
          event.currentTarget.value = ''
          void images.upload(files)
        }}
      />
      <Button
        variant="ghost"
        size="icon-sm"
        label={images.attachments.length >= maxAttachments ? `At most ${maxAttachments} images` : 'Attach an image'}
        disabled={disabled || images.uploading || images.attachments.length >= maxAttachments}
        onClick={() => picker.current?.click()}
      >
        <Paperclip />
      </Button>
      {images.attachments.map((path, index) => (
        <Button
          key={path}
          variant="ghost"
          size="sm"
          aria-label={`Remove attached image ${index + 1}`}
          hint={path}
          disabled={disabled || images.uploading}
          onClick={() => images.remove(index)}
        >
          Image {index + 1}
          <X />
        </Button>
      ))}
    </>
  )
}

export function imageMessage(body: string, attachments: string[]): string {
  const text = body.trim()
  if (!attachments.length) return text
  // Match internal/collab/delivery.go's agent-facing file-reference framing.
  return `${text || 'Please inspect the attached images.'}\n\n--- AETHER ATTACHMENTS ---\n${attachments.map((path) => `- ${path}\n`).join('')}--- END AETHER ATTACHMENTS ---`
}
