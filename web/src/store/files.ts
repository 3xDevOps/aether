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

/**
 * A draft is deliberately kept outside persistedUi. It is the user's in-memory
 * working copy, together with the server revision it was based on. Keeping the
 * original content lets a save response that races with new typing preserve
 * the newer edits instead of marking them clean.
 */
export interface FileDraftState {
  content: string
  baseContent: string
  baseRevision: string
  saving?: boolean
  conflict?: boolean
  error?: string
}

export interface FileDiffState {
  patch: string
  truncated: boolean
  loading?: boolean
  error?: string
}

export interface WorkspaceFileTab {
  key: string
  kind: 'workspace'
  workspaceID: string
  runID: string
  path: string
  label: string
  branch?: string
}

export interface ConfigFileTab {
  key: string
  kind: 'config'
  harness: string
  rootPath: string
  path: string
  label: string
}

export type FileTab = WorkspaceFileTab | ConfigFileTab

export interface FilesSlice {
  trees: Record<string, FileTreeState>
  documents: Record<string, FileDocumentState>
  drafts: Record<string, FileDraftState>
  fileDiffs: Record<string, FileDiffState>
  fileTabs: FileTab[]
  activeFileKey: string | null
  /** Incremented whenever cached viewer data is invalidated. */
  filesEpoch: number
  /** Incremented only when the authenticated server/member changes. */
  identityEpoch: number
  /** Drops every file/config cache at an authenticated identity boundary. */
  resetFiles: () => void
  setTree: (key: string, patch: FileTreeState) => void
  invalidateTree: (key: string) => void
  setDocument: (key: string, patch: FileDocumentState) => void
  setDraft: (key: string, patch: Partial<FileDraftState> & { content: string }) => void
  updateDraft: (key: string, content: string) => void
  clearDraft: (key: string) => void
  openFileTab: (tab: FileTab) => void
  closeFileTab: (key: string) => void
  setActiveFileKey: (key: string | null) => void
  markDraftSaved: (
    key: string,
    capturedContent: string,
    result: FileRead,
  ) => void
  markDraftError: (key: string, error: string, conflict?: boolean) => void
  setFileDiff: (key: string, patch: FileDiffState) => void
  invalidateRun: (runID: string) => void
}

/** A collision-free cache key for one workspace/run/path request. */
export function filesKey(workspaceID: string, runID: string, path: string): string {
  return [workspaceID, runID, path].join('\u0000')
}

/** A cache key for a member-owned persistent config root. */
export function configKey(harness: string, path: string): string {
  return ['config', harness, path].join('\u0000')
}

function belongsToRun(key: string, runID: string): boolean {
  const parts = key.split('\u0000', 3)
  return parts[0] !== 'config' && parts[1] === runID
}

export const createFilesSlice: SliceCreator<FilesSlice> = (set) => ({
  trees: {},
  documents: {},
  drafts: {},
  fileDiffs: {},
  fileTabs: [],
  activeFileKey: null,
  filesEpoch: 0,
  identityEpoch: 0,
  setTree: (key, patch) =>
    set((s) => ({ trees: { ...s.trees, [key]: { ...(s.trees[key] ?? { entries: [] }), ...patch } } })),
  invalidateTree: (key) =>
    set((s) => {
      if (!s.trees[key]) return s
      const trees = { ...s.trees }
      delete trees[key]
      return { trees }
    }),
  setDocument: (key, patch) =>
    set((s) => ({
      documents: {
        ...s.documents,
        [key]: {
          ...(s.documents[key] ?? {
            content: '',
            truncated: false,
            binary: false,
            size: 0,
            revision: '',
            writable: false,
          }),
          ...patch,
        },
      },
    })),
  setDraft: (key, patch) =>
    set((s) => {
      const document = s.documents[key]
      const previous = s.drafts[key]
      const baseContent = patch.baseContent ?? previous?.baseContent ?? document?.content ?? ''
      const baseRevision = patch.baseRevision ?? previous?.baseRevision ?? document?.revision ?? ''
      return {
        drafts: {
          ...s.drafts,
          [key]: {
            content: patch.content,
            baseContent,
            baseRevision,
            saving: patch.saving ?? previous?.saving,
            conflict: patch.conflict ?? previous?.conflict,
            error: patch.error ?? previous?.error,
          },
        },
      }
    }),
  updateDraft: (key, content) =>
    set((s) => {
      const document = s.documents[key]
      const previous = s.drafts[key]
      const baseContent = previous?.baseContent ?? document?.content ?? ''
      const baseRevision = previous?.baseRevision ?? document?.revision ?? ''
      if (content === baseContent) {
        if (!previous) return s
        if (previous.saving) {
          return {
            drafts: {
              ...s.drafts,
              [key]: { ...previous, content },
            },
          }
        }
        const drafts = { ...s.drafts }
        delete drafts[key]
        return { drafts }
      }
      return {
        drafts: {
          ...s.drafts,
          [key]: {
            content,
            baseContent,
            baseRevision,
            saving: previous?.saving,
            conflict: false,
            error: undefined,
          },
        },
      }
    }),
  clearDraft: (key) =>
    set((s) => {
      if (!s.drafts[key]) return s
      const drafts = { ...s.drafts }
      delete drafts[key]
      return { drafts }
    }),
  openFileTab: (tab) =>
    set((s) => ({
      fileTabs: s.fileTabs.some((current) => current.key === tab.key)
        ? s.fileTabs.map((current) => (current.key === tab.key ? tab : current))
        : [...s.fileTabs, tab],
      activeFileKey: tab.key,
    })),
  closeFileTab: (key) =>
    set((s) => {
      const fileTabs = s.fileTabs.filter((tab) => tab.key !== key)
      return {
        fileTabs,
        activeFileKey:
          s.activeFileKey === key
            ? fileTabs.at(-1)?.key ?? null
            : s.activeFileKey,
      }
    }),
  setActiveFileKey: (key) => set({ activeFileKey: key }),
  markDraftSaved: (key, capturedContent, result) =>
    set((s) => {
      const draft = s.drafts[key]
      const documents = { ...s.documents, [key]: { ...result, loading: false, error: undefined } }
      if (!draft || draft.content === capturedContent) {
        if (!draft) return { documents }
        const drafts = { ...s.drafts }
        delete drafts[key]
        return { documents, drafts }
      }
      return {
        documents,
        drafts: {
          ...s.drafts,
          [key]: {
            ...draft,
            baseContent: capturedContent,
            baseRevision: result.revision,
            saving: false,
            conflict: false,
            error: undefined,
          },
        },
      }
    }),
  markDraftError: (key, error, conflict = false) =>
    set((s) => {
      const draft = s.drafts[key]
      if (!draft) return s
      return {
        drafts: {
          ...s.drafts,
          [key]: { ...draft, saving: false, conflict, error },
        },
      }
    }),
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
  resetFiles: () =>
    set((s) => ({
      trees: {},
      documents: {},
      drafts: {},
      fileDiffs: {},
      fileTabs: [],
      activeFileKey: null,
      filesEpoch: s.filesEpoch + 1,
      identityEpoch: s.identityEpoch + 1,
    })),
})

function withoutRun<T>(values: Record<string, T>, runID: string): Record<string, T> {
  return Object.fromEntries(Object.entries(values).filter(([key]) => !belongsToRun(key, runID)))
}
