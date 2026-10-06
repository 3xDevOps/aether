import type { Run } from '@/lib/types'
import { configKey, filesKey, type FileTab } from '@/store/files'

export interface WorkspaceSource {
  kind: 'workspace'
  workspaceID: string
  runID: string
  label: string
  branch?: string
}

export interface ConfigSource {
  kind: 'config'
  harness: string
  rootPath: string
  label: string
}

export type FileSource = WorkspaceSource | ConfigSource

export type Selection = FileSource & { path: string }

export type SelectFile = (source: FileSource, path: string) => void

export function sourceKey(source: FileSource, path: string): string {
  return source.kind === 'config'
    ? configKey(source.harness, path)
    : filesKey(source.workspaceID, source.runID, path)
}

export function sourceLabel(source: FileSource): string {
  return source.kind === 'config' ? `${source.label} · ${source.rootPath}` : source.label
}

export function isBaseBranch(source: FileSource): source is WorkspaceSource {
  return source.kind === 'workspace' && !source.runID
}

export function fileTabFromSource(source: FileSource, path: string, key: string): FileTab {
  return source.kind === 'config'
    ? { key, kind: 'config', harness: source.harness, rootPath: source.rootPath, path, label: source.label }
    : { key, kind: 'workspace', workspaceID: source.workspaceID, runID: source.runID, path, label: source.label, branch: source.branch }
}

export function fileTabToSelection(tab: FileTab): Selection {
  return tab.kind === 'config'
    ? { kind: 'config', harness: tab.harness, rootPath: tab.rootPath, path: tab.path, label: tab.label }
    : { kind: 'workspace', workspaceID: tab.workspaceID, runID: tab.runID, path: tab.path, label: tab.label, branch: tab.branch }
}

export function isLiveRun(run: Run): boolean {
  return !['merged', 'abandoned', 'failed', 'interrupted'].includes(run.status)
}
