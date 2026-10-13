import { useState } from 'react'
import { ApiError, readFileBase64, type Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import { childPath } from '@/routes/files/sources'
import { sameIdentity, useStore } from '@/store'

/** The server's per-file ceiling, checked here so a larger file is never read. */
const maxUploadBytes = 64 << 20

/** One file and where it lands, relative to the folder it was dropped on. */
export interface Upload {
  file: File
  path: string
}

export interface UploadState {
  /** The file being sent, counted from one. */
  progress: { at: number; of: number; name: string } | null
  /** Files the server already has, waiting for Replace or Skip. */
  conflicts: { dir: string; uploads: Upload[] } | null
  /** One sentence per file that was not uploaded, naming its path. */
  failures: string[]
}

function entryFile(entry: FileSystemFileEntry): Promise<File> {
  return new Promise((resolve, reject) => entry.file(resolve, reject))
}

async function walk(entry: FileSystemEntry, dir: string, out: Upload[]): Promise<void> {
  const path = childPath(dir, entry.name)
  if (entry.isFile) {
    out.push({ file: await entryFile(entry as FileSystemFileEntry), path })
    return
  }
  const reader = (entry as FileSystemDirectoryEntry).createReader()
  // readEntries returns a directory in batches and an empty one at its end.
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject))
    if (batch.length === 0) return
    for (const child of batch) await walk(child, path, out)
  }
}

/** The files of a drop, with dropped folders walked so their structure is
 * kept. A file dragged out of another application has no entry, only its
 * bytes. Both are taken before the first await: a drop's items are gone once
 * its handler returns. */
export async function droppedUploads(data: DataTransfer): Promise<Upload[]> {
  const dropped = [...data.items]
    .filter((item) => item.kind === 'file')
    .map((item) => item.webkitGetAsEntry() ?? item.getAsFile())
  const out: Upload[] = []
  for (const item of dropped) {
    if (item instanceof File) out.push({ file: item, path: item.name })
    else if (item) await walk(item, '', out)
  }
  return out
}

/** Sends files one at a time, one batch at a time. A file the server already
 * has is not replaced: it is collected, and asked about once the rest are sent. */
export function useHomeUpload(client: Api) {
  const [state, setState] = useState<UploadState>({ progress: null, conflicts: null, failures: [] })

  const send = async (dir: string, pending: Upload[] | Promise<Upload[]>, overwrite: boolean) => {
    // Walking a folder and reading a file both outlast a change of member,
    // and the bytes must not follow to the next one's home.
    const same = sameIdentity()
    const conflicts: Upload[] = []
    const failures: string[] = []
    let uploads: Upload[] = []
    if (Array.isArray(pending)) uploads = pending
    else {
      setState((s) => ({ ...s, progress: { at: 0, of: 0, name: '' } }))
      try {
        uploads = await pending
      } catch (err) {
        failures.push(errorSentence(err))
      }
      if (!same()) return
    }
    for (const [index, upload] of uploads.entries()) {
      const path = childPath(dir, upload.path)
      setState((s) => ({ ...s, conflicts: null, progress: { at: index + 1, of: uploads.length, name: upload.path } }))
      try {
        if (upload.file.size > maxUploadBytes) throw new Error(`${path} is over the 64 MiB limit`)
        // Safari reads an empty file as a data URL with no payload separator.
        const content = upload.file.size === 0 ? '' : await readFileBase64(upload.file)
        if (!same()) return
        await client.homeUpload(path, content, overwrite)
      } catch (err) {
        const reason = errorSentence(err)
        if (!overwrite && err instanceof ApiError && err.status === 409) conflicts.push(upload)
        else failures.push(reason.startsWith(path) ? reason : `${path}: ${reason}`)
      }
      if (!same()) return
    }
    if (uploads.length > 0) useStore.getState().refreshHome()
    setState((s) => ({
      progress: null,
      conflicts: conflicts.length > 0 ? { dir, uploads: conflicts } : null,
      failures: [...s.failures, ...failures],
    }))
  }

  const busy = state.progress !== null || state.conflicts !== null
  return {
    state,
    busy,
    /** Takes the batch at once, before a dropped folder has been walked. */
    upload: (dir: string, uploads: Upload[] | Promise<Upload[]>) => {
      if (!busy) void send(dir, uploads, false)
    },
    replace: () => {
      if (state.conflicts) void send(state.conflicts.dir, state.conflicts.uploads, true)
    },
    skip: () => setState((s) => ({ ...s, conflicts: null })),
    dismissFailures: () => setState((s) => ({ ...s, failures: [] })),
  }
}
