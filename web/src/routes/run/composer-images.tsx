import { useEffect, useState } from 'react'
import type * as React from 'react'
import { api, isTerminalImageType, MAX_TERMINAL_FILE_BYTES, MAX_TERMINAL_IMAGE_BYTES } from '@/lib/api'
import { message } from '@/lib/format'
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

const mib = (bytes: number) => `${bytes / 1024 / 1024} MiB`
const sizeLimits = `Images up to ${mib(MAX_TERMINAL_IMAGE_BYTES)}, other files up to ${mib(MAX_TERMINAL_FILE_BYTES)}`
export const limitsHint = `${sizeLimits} · at most ${maxAttachments}`

export const attachmentName = (image: ComposerImage): string => image.file.name || (isImage(image) ? 'Clipboard image' : 'Clipboard file')

/** The refusal for a file the server would refuse, so it never waits in the draft to fail. */
function oversize(file: File): boolean {
  return file.size > (isTerminalImageType(file.type) ? MAX_TERMINAL_IMAGE_BYTES : MAX_TERMINAL_FILE_BYTES)
}

function oversizeNotice(files: File[]): string | undefined {
  if (files.length === 0) return undefined
  if (files.length > 1) return `${files.length} files are over the size limit. ${sizeLimits}.`
  const [file] = files
  return isTerminalImageType(file.type)
    ? `${file.name || 'Clipboard image'} is over the ${mib(MAX_TERMINAL_IMAGE_BYTES)} image limit.`
    : `${file.name || 'Clipboard file'} is over the ${mib(MAX_TERMINAL_FILE_BYTES)} limit.`
}

export type AttachmentState =
  | { status: 'attached' | 'uploading' | 'waiting' | 'idle' }
  | { status: 'failed'; reason: string }

const uploadFailed = 'Upload failed: '

/**
 * Where each file stands. Uploads run in order and stop at the first failure,
 * so while one runs the files before it are attached and the rest wait; after a
 * failure the first unattached file carries the reason and the rest never ran.
 */
export function attachmentStates(previews: ComposerImage[], uploading: boolean, uploadError: string | undefined): AttachmentState[] {
  let next = true
  return previews.map((image) => {
    if (image.path) return { status: 'attached' }
    const first = next
    next = false
    if (uploading) return { status: first ? 'uploading' : 'waiting' }
    if (first && uploadError?.startsWith(uploadFailed)) return { status: 'failed', reason: uploadError.slice(uploadFailed.length) }
    return { status: 'idle' }
  })
}

/** An object URL for an image attachment, revoked when it goes. */
export function usePreviewURL(image: ComposerImage): string | undefined {
  const picture = isImage(image)
  const [url, setURL] = useState<string>()
  useEffect(() => {
    if (!picture || typeof URL.createObjectURL !== 'function') return
    const created = URL.createObjectURL(image.file)
    setURL(created)
    return () => URL.revokeObjectURL(created)
  }, [image.file, picture])
  return url
}

export interface ComposerImageState {
  attachments: string[]
  previews: ComposerImage[]
  uploading: boolean
  ready: boolean
  uploadError: string | undefined
  upload: (files: File[]) => Promise<void>
  retry: () => Promise<void>
  remove: (index: number) => void
  clear: () => void
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
  const clear = () => {
    const draft = composerDraft(runID)
    if (draft.busy || draft.uploading) return
    saveComposerDraft(runID, { images: [], uploadError: undefined })
  }
  const start = async (notice?: string) => {
    const draft = composerDraft(runID)
    if (!enabled || draft.busy || draft.uploading) return
    const request = beginDraftRequest(runID, 'uploading', { uploadError: notice })
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
      request.settle({ uploadError: `${uploadFailed}${message(cause)}` })
    }
  }
  const upload = async (files: File[]) => {
    const draft = composerDraft(runID)
    if (!files.length || !enabled || draft.busy || draft.uploading) return
    const refused = files.filter(oversize)
    const accepted = files.filter((file) => !oversize(file))
    if (accepted.length === 0) {
      saveComposerDraft(runID, { uploadError: oversizeNotice(refused) })
      return
    }
    if (draft.images.length + accepted.length > maxAttachments) {
      saveComposerDraft(runID, { uploadError: `Attach at most ${maxAttachments} files.` })
      return
    }
    saveComposerDraft(runID, { images: [...draft.images, ...accepted.map((file) => ({ id: nextID++, file }))] })
    await start(oversizeNotice(refused))
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
    uploadError, upload, retry: () => start(), remove, clear, onPaste,
    clearError: () => saveComposerDraft(runID, { uploadError: undefined }),
    isUploading: () => Boolean(composerDraft(runID).uploading),
    isReady: () => !composerDraft(runID).uploading && composerDraft(runID).images.every((image) => Boolean(image.path)),
    getPaths: () => pathsOf(composerDraft(runID).images),
  }
}
