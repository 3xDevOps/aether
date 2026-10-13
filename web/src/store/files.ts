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

export interface HomeFileTab {
  key: string
  kind: 'home'
  path: string
  label: string
}

export type FileTab = WorkspaceFileTab | HomeFileTab

export interface FilesSlice {
  trees: Record<string, FileTreeState>
  documents: Record<string, FileDocumentState>
  drafts: Record<string, FileDraftState>
  fileDiffs: Record<string, FileDiffState>
  fileTabs: FileTab[]
  activeFileKey: string | null
  /** Every move and delete of a Home path since the last identity change. */
  homeMoves: HomeMove[]
  /** Incremented whenever cached viewer data is invalidated. */
  filesEpoch: number
  /** Incremented only when the authenticated server/member changes. */
  identityEpoch: number
  /** Drops every file cache at an authenticated identity boundary. */
  resetFiles: () => void
  setTree: (key: string, patch: FileTreeState) => void
  invalidateTree: (key: string) => void
  /** Drops every listing of the member's home, so open folders are reread. */
  refreshHome: () => void
  /** Carries the tabs, documents, drafts and listings of a renamed or moved
   * home path, and of everything under it, to its new path. */
  moveHomePath: (from: string, to: string) => void
  /** Closes the tabs of a deleted home path and of everything under it, and
   * drops their documents, drafts and listings. */
  dropHomePath: (path: string) => void
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

const homePrefix = 'home\u0000'

/** A cache key for a path in the member's persistent home. */
export function homeKey(path: string): string {
  return homePrefix + path
}

function homePath(key: string): string | null {
  return key.startsWith(homePrefix) ? key.slice(homePrefix.length) : null
}

function belongsToRun(key: string, runID: string): boolean {
  return homePath(key) === null && key.split('\u0000', 3)[1] === runID
}

const within = (path: string, dir: string) => path === dir || path.startsWith(`${dir}/`)

/** Where a home path lands when `from` becomes `to`: null when it is dropped,
 * because `from` was deleted or the path was a stale leftover under `to`. */
function relocate(path: string, from: string, to?: string): string | null {
  if (within(path, from)) return to === undefined ? null : to + path.slice(from.length)
  return to !== undefined && within(path, to) ? null : path
}

/** Moves the entries under `from`. A moved entry that fails `carry` is left
 * behind instead. */
function relocateKeys<T>(values: Record<string, T>, from: string, to?: string, carry: (value: T) => boolean = () => true): Record<string, T> {
  const out: Record<string, T> = {}
  for (const [key, value] of Object.entries(values)) {
    const path = homePath(key)
    const next = path === null ? path : relocate(path, from, to)
    if (next === path) out[key] = value
    else if (next !== null && carry(value)) out[homeKey(next)] = value
  }
  return out
}

/** A listing or read in flight answers for the old path, so it is not moved
 * and its reader asks again at the new one. */
const settled = (value: { loading?: boolean }) => !value.loading

/** One rename, move, or (without `to`) delete of a Home path. */
export interface HomeMove {
  from: string
  to?: string
}

/** Where the file a key named is after `moves`, or null once it is deleted.
 * A save that outlasts a rename settles there. */
export function followHomeKey(moves: HomeMove[], key: string): string | null {
  let path = homePath(key)
  if (path === null) return key
  for (const move of moves) {
    const next = relocate(path, move.from, move.to)
    if (next === null) return null
    path = next
  }
  return homeKey(path)
}

function relocateHome(s: FilesSlice, from: string, to?: string): Partial<FilesSlice> {
  const fileTabs = s.fileTabs.flatMap((tab): FileTab[] => {
    if (tab.kind !== 'home') return [tab]
    const path = relocate(tab.path, from, to)
    return path === null ? [] : [{ ...tab, path, key: homeKey(path) }]
  })
  const activePath = s.activeFileKey === null ? null : homePath(s.activeFileKey)
  const active = activePath === null ? s.activeFileKey : relocate(activePath, from, to)
  return {
    fileTabs,
    activeFileKey: activePath === null ? s.activeFileKey : active === null ? fileTabs.at(-1)?.key ?? null : homeKey(active),
    trees: relocateKeys(s.trees, from, to, settled),
    documents: relocateKeys(s.documents, from, to, settled),
    drafts: relocateKeys(s.drafts, from, to),
    homeMoves: [...s.homeMoves, { from, to }],
  }
}

export const createFilesSlice: SliceCreator<FilesSlice> = (set) => ({
  trees: {},
  documents: {},
  drafts: {},
  fileDiffs: {},
  fileTabs: [],
  activeFileKey: null,
  homeMoves: [],
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
  refreshHome: () =>
    set((s) => ({ trees: Object.fromEntries(Object.entries(s.trees).filter(([key]) => homePath(key) === null)) })),
  moveHomePath: (from, to) => set((s) => relocateHome(s, from, to)),
  dropHomePath: (path) => set((s) => relocateHome(s, path)),
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
      homeMoves: [],
      filesEpoch: s.filesEpoch + 1,
      identityEpoch: s.identityEpoch + 1,
    })),
})

function withoutRun<T>(values: Record<string, T>, runID: string): Record<string, T> {
  return Object.fromEntries(Object.entries(values).filter(([key]) => !belongsToRun(key, runID)))
}
