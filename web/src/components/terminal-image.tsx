import type * as React from 'react'
import { ImageUp, Loader2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import type { Terminal } from '@xterm/xterm'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { api, MAX_TERMINAL_IMAGE_BYTES, TERMINAL_IMAGE_TYPES } from '@/lib/api'
import { message } from '@/lib/format'
import { pasteClipboard as pasteClipboardTextOrImage, registerClipboardImages } from '@/lib/term-clipboard'
import { useCapability } from '@/store/hooks'

const imageAccept = TERMINAL_IMAGE_TYPES.join(',')

type TerminalImageIdentity = {
  terminal: Terminal | null
  target: string | undefined
  key: string | undefined
  enabled: boolean
}

export type TerminalImageProps = {
  terminal: Terminal | null
  imageTarget?: string
  imageTargetKey?: string
  imageUploadEnabled?: boolean
  focusTerminal: () => void
}
export type TerminalImageController = {
  canUpload: boolean
  openPicker: () => void
  pasteImages: (files: File[]) => Promise<void>
  pasteClipboard: () => Promise<void>
  dialog: React.ReactNode
}

function shellQuote(path: string): string {
  return `'${path.replaceAll("'", "'\\''")}'`
}


function sameIdentity(a: TerminalImageIdentity, b: TerminalImageIdentity): boolean {
  return a.terminal === b.terminal && a.target === b.target && a.key === b.key && a.enabled === b.enabled
}

export function useTerminalImage({
  terminal,
  imageTarget,
  imageTargetKey,
  imageUploadEnabled = true,
  focusTerminal,
}: TerminalImageProps): TerminalImageController {
  const capability = useCapability()
  const uploadEnabled = imageUploadEnabled
  const canUpload =
    terminal !== null && uploadEnabled && capability.hasMethod('terminal.image')
  const picker = useRef<HTMLInputElement>(null)
  const canUploadRef = useRef(canUpload)
  canUploadRef.current = canUpload
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState<File | null>(null)
  const [preview, setPreview] = useState<string | null>(null)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const identityRef = useRef<TerminalImageIdentity>({
    terminal: null,
    target: undefined,
    key: undefined,
    enabled: true,
  })
  const pickerRequest = useRef<{ identity: TerminalImageIdentity; token: number } | null>(null)
  const generation = useRef(0)
  const identity = {
    terminal,
    target: imageTarget,
    key: imageTargetKey,
    enabled: uploadEnabled,
  }
  const identityChanged = !sameIdentity(identityRef.current, identity)
  identityRef.current = identity

  useEffect(() => {
    if (!identityChanged) return
    generation.current += 1
    pickerRequest.current = null
    setOpen(false)
    setSelected(null)
    setError(null)
    setUploading(false)
  }, [identityChanged, terminal, imageTarget, imageTargetKey, uploadEnabled])

  useEffect(() => {
    return () => {
      generation.current += 1
    }
  }, [])

  useEffect(() => {
    if (!selected) {
      setPreview(null)
      return
    }
    if (typeof URL.createObjectURL !== 'function') {
      setPreview(null)
      return
    }
    const url = URL.createObjectURL(selected)
    setPreview(url)
    return () => URL.revokeObjectURL(url)
  }, [selected])

  const openFiles = useCallback(
    (files: File[]) => {
      if (!canUpload || uploading) return
      if (files.length > 1) {
        setSelected(null)
        setError('Choose one image at a time.')
        setOpen(true)
        return
      }
      const file = files.find((candidate) => candidate.type.startsWith('image/')) ?? files[0] ?? null
      if (!file) return
      setSelected(file)
      setError(validateTerminalImage(file))
      setOpen(true)
    },
    [canUpload, uploading],
  )

  const openPicker = useCallback(() => {
    if (!canUpload) return
    pickerRequest.current = {
      identity: identityRef.current,
      token: generation.current,
    }
    picker.current?.click()
  }, [canUpload])

  const pasteImages = useCallback(
    (files: File[]) => {
      openFiles(files)
      return Promise.resolve()
    },
    [openFiles],
  )
  useEffect(() => {
    if (!terminal || !canUpload) return
    const started = identityRef.current
    const token = generation.current
    return registerClipboardImages(terminal, (files) => {
      if (
        !canUploadRef.current ||
        token !== generation.current ||
        !sameIdentity(identityRef.current, started)
      ) {
        return Promise.resolve()
      }
      return pasteImages(files)
    })
  }, [
    canUpload,
    imageTarget,
    imageTargetKey,
    pasteImages,
    terminal,
    uploadEnabled,
  ])

  const pasteClipboardImages = useCallback(() => {
    if (!terminal || !canUpload) return Promise.resolve()
    const started = identityRef.current
    const token = generation.current
    return pasteClipboardTextOrImage(terminal, (files) => {
      if (
        !canUploadRef.current ||
        token !== generation.current ||
        !sameIdentity(identityRef.current, started)
      ) {
        return Promise.resolve()
      }
      return pasteImages(files)
    })
  }, [canUpload, pasteImages, terminal])

  const close = useCallback(() => {
    generation.current += 1
    setOpen(false)
    setSelected(null)
    setError(null)
    setUploading(false)
  }, [])

  const upload = useCallback(async () => {
    if (!selected || !terminal || !canUpload || uploading) return
    const started: TerminalImageIdentity = {
      terminal,
      target: imageTarget,
      key: imageTargetKey,
      enabled: uploadEnabled,
    }
    const token = ++generation.current
    setUploading(true)
    setError(null)
    try {
      const result = await api.uploadTerminalImage(selected, imageTarget)
      if (
        token !== generation.current ||
        !canUploadRef.current ||
        !sameIdentity(identityRef.current, started)
      ) {
        return
      }
      terminal.paste(shellQuote(result.path))
      close()
      focusTerminal()
    } catch (err) {
      if (token === generation.current) setError(`Upload failed: ${message(err)}`)
    } finally {
      if (token === generation.current) setUploading(false)
    }
  }, [
    canUpload,
    close,
    focusTerminal,
    imageTarget,
    imageTargetKey,
    selected,
    terminal,
    uploadEnabled,
    uploading,
  ])

  const onFileChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.currentTarget.files ?? [])
    const request = pickerRequest.current
    pickerRequest.current = null
    event.currentTarget.value = ''
    if (
      request &&
      request.token === generation.current &&
      sameIdentity(identityRef.current, request.identity)
    ) {
      openFiles(files)
    }
  }

  const selectedError = selected ? validateTerminalImage(selected) : null
  const dialog = (
    <>
      <input
        ref={picker}
        type="file"
        accept={imageAccept}
        className="sr-only"
        tabIndex={-1}
        onChange={onFileChange}
      />
      <Dialog
        open={open}
        onOpenChange={(next) => {
          if (!next) close()
        }}
      >
        <DialogContent className="max-w-[min(480px,calc(100%-2rem))] p-0">
          <DialogHeader className="min-w-0 border-b px-3 py-3 pr-10 sm:px-4">
            <DialogTitle>Upload image to terminal</DialogTitle>
            <DialogDescription>
              The image is copied to the remote terminal home, then its safely quoted path is pasted without running it.
            </DialogDescription>
          </DialogHeader>
          <div className="min-w-0 space-y-3 px-3 py-3 sm:px-4">
            <p className="text-xs leading-4 text-muted-foreground">
              Some clipboard managers provide only a client-local path. The remote terminal cannot read that path; choose the actual image file here. Supported formats: PNG, JPEG, GIF, and WebP, up to 8 MiB.
            </p>
            {selected && (
              <div className="flex min-w-0 items-center gap-3 rounded-[2px] border border-border bg-background p-2">
                {preview ? (
                  <img className="size-16 shrink-0 rounded-[2px] border border-border object-contain" src={preview} alt="Selected image preview" />
                ) : (
                  <span className="grid size-16 shrink-0 place-items-center rounded-[2px] border border-border text-xs text-muted-foreground">Image</span>
                )}
                <span className="min-w-0 break-words text-sm">{selected.name || 'Clipboard image'}</span>
              </div>
            )}
            <Button type="button" variant="outline" onClick={openPicker} disabled={uploading}>
              Choose another image
            </Button>
            {error && (
              <p role="alert" className="break-words text-xs text-state-failed">
                {error}
              </p>
            )}
          </div>
          <DialogFooter className="border-t px-3 py-3 sm:px-4">
            <Button type="button" variant="outline" onClick={close}>
              Cancel
            </Button>
            <Button
              type="button"
              onClick={() => void upload()}
              disabled={!selected || !!selectedError || uploading}
            >
              {uploading && (
                <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden />
              )}
              {uploading ? 'Uploading...' : 'Upload and insert'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )

  return { canUpload, openPicker, pasteImages, pasteClipboard: pasteClipboardImages, dialog }
}

export function TerminalImageAction({ controller }: { controller: TerminalImageController }) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      aria-label="Upload image to terminal"
      title="Upload image to terminal"
      disabled={!controller.canUpload}
      onClick={controller.openPicker}
    >
      <ImageUp />
    </Button>
  )
}

export function validateTerminalImage(file: File): string | null {
  if (!(TERMINAL_IMAGE_TYPES as readonly string[]).includes(file.type)) {
    return 'Choose a PNG, JPEG, GIF, or WebP image.'
  }
  if (file.size > MAX_TERMINAL_IMAGE_BYTES) return 'Image must be 8 MiB or smaller.'
  return null
}
