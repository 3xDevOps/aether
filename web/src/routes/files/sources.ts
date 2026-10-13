import type { Run } from '@/lib/types'
import { filesKey, homeKey, type FileTab } from '@/store/files'

export interface WorkspaceSource {
  kind: 'workspace'
  workspaceID: string
  runID: string
  label: string
  branch?: string
}

/** The member's persistent home, `~` in their Environment and runs. */
export interface HomeSource {
  kind: 'home'
  label: string
}

export const homeSource: HomeSource = { kind: 'home', label: 'Home' }

export type FileSource = WorkspaceSource | HomeSource

export type Selection = FileSource & { path: string }

export type SelectFile = (source: FileSource, path: string) => void

export function sourceKey(source: FileSource, path: string): string {
  return source.kind === 'home' ? homeKey(path) : filesKey(source.workspaceID, source.runID, path)
}

/** A home-relative path as it is typed in a shell. */
export const tildePath = (path: string) => (path ? `~/${path}` : '~')

export const parentPath = (path: string) => path.split('/').slice(0, -1).join('/')

export const childPath = (dir: string, name: string) => (dir ? `${dir}/${name}` : name)

export const baseName = (path: string) => path.split('/').at(-1) ?? path

export function isBaseBranch(source: FileSource): source is WorkspaceSource {
  return source.kind === 'workspace' && !source.runID
}

export function fileTabFromSource(source: FileSource, path: string, key: string): FileTab {
  return source.kind === 'home'
    ? { key, kind: 'home', path, label: source.label }
    : { key, kind: 'workspace', workspaceID: source.workspaceID, runID: source.runID, path, label: source.label, branch: source.branch }
}

export function fileTabToSelection(tab: FileTab): Selection {
  return tab.kind === 'home'
    ? { kind: 'home', path: tab.path, label: tab.label }
    : { kind: 'workspace', workspaceID: tab.workspaceID, runID: tab.runID, path: tab.path, label: tab.label, branch: tab.branch }
}

export function isLiveRun(run: Run): boolean {
  return !['merged', 'abandoned', 'failed', 'interrupted'].includes(run.status)
}
