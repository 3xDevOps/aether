import type { FileRead, FileTreeEntry } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export interface FileTreeState {
  entries: FileTreeEntry[]
  loading?: boolean
  error?: string
}

export interface FileDocumentState extends FileRead {
  loading?: boolean
  error?: string
}

export interface FileDiffState {
  patch: string
  truncated: boolean
  loading?: boolean
  error?: string
}

export interface FilesSlice {
  trees: Record<string, FileTreeState>
  documents: Record<string, FileDocumentState>
  fileDiffs: Record<string, FileDiffState>
  /** Incremented whenever a run invalidation clears viewer data. */
  filesEpoch: number
  setTree: (key: string, patch: FileTreeState) => void
  setDocument: (key: string, patch: FileDocumentState) => void
  setFileDiff: (key: string, patch: FileDiffState) => void
  invalidateRun: (runID: string) => void
}

/** A collision-free cache key for one workspace/run/path request. */
export function filesKey(workspaceID: string, runID: string, path: string): string {
  return [workspaceID, runID, path].join('\u0000')
}

function belongsToRun(key: string, runID: string): boolean {
  return key.split('\u0000', 3)[1] === runID
}

export const createFilesSlice: SliceCreator<FilesSlice> = (set) => ({
  trees: {},
  documents: {},
  fileDiffs: {},
  filesEpoch: 0,
  setTree: (key, patch) =>
    set((s) => ({ trees: { ...s.trees, [key]: { ...(s.trees[key] ?? { entries: [] }), ...patch } } })),
  setDocument: (key, patch) =>
    set((s) => ({
      documents: { ...s.documents, [key]: { ...(s.documents[key] ?? { content: '', truncated: false, binary: false, size: 0 }), ...patch } },
    })),
  setFileDiff: (key, patch) =>
    set((s) => ({
      fileDiffs: { ...s.fileDiffs, [key]: { ...(s.fileDiffs[key] ?? { patch: '', truncated: false }), ...patch } },
    })),
  invalidateRun: (runID) =>
    set((s) => ({
      trees: withoutRun(s.trees, runID),
      documents: withoutRun(s.documents, runID),
      fileDiffs: withoutRun(s.fileDiffs, runID),
      filesEpoch: s.filesEpoch + 1,
    })),
})

function withoutRun<T>(values: Record<string, T>, runID: string): Record<string, T> {
  return Object.fromEntries(Object.entries(values).filter(([key]) => !belongsToRun(key, runID)))
}
