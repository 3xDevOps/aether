import { useEffect, useMemo } from 'react'
import { create } from 'zustand'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { parsePatch, type PatchFile } from '@/routes/diff/parse'
import { useStore } from '@/store'

interface RepoPatch {
  patch: string
  truncated: boolean
  loading: boolean
  error?: string
  revision: number
}

const usePatches = create<Record<string, RepoPatch>>(() => ({}))
const inflight = new Set<string>()
const keyOf = (runID: string, repo: string) => `${runID}#${repo}`

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) usePatches.setState({}, true)
})

export async function loadRepoPatch(runID: string, repo: string, client: Pick<Api, 'runPatch'> = api): Promise<void> {
  const key = keyOf(runID, repo)
  if (inflight.has(key)) return
  inflight.add(key)
  const diff = useStore.getState().diffs[runID]
  const revision = (diff?.revision ?? 0) + (diff?.repoRevision ?? 0)
  usePatches.setState((all) => ({ [key]: { ...(all[key] ?? { patch: '', truncated: false }), loading: true, revision } }))
  try {
    const result = await client.runPatch(runID, undefined, repo)
    usePatches.setState((all) => ({ [key]: { ...all[key]!, patch: result.patch, truncated: result.truncated, loading: false, error: undefined } }))
  } catch (cause) {
    usePatches.setState((all) => ({ [key]: { ...all[key]!, loading: false, error: message(cause) } }))
  } finally {
    inflight.delete(key)
  }
}

export interface RepoChangesData {
  files: PatchFile[]
  truncated: boolean
  loading: boolean
  error?: string
  refresh: () => void
}

export function useRepoPatch(runID: string, repo: string, enabled = true): RepoChangesData {
  const entry = usePatches((all) => all[keyOf(runID, repo)])
  const revision = useStore((s) => (s.diffs[runID]?.revision ?? 0) + (s.diffs[runID]?.repoRevision ?? 0))
  useEffect(() => {
    if (enabled && !entry?.loading && entry?.revision !== revision) void loadRepoPatch(runID, repo)
  }, [enabled, runID, repo, revision, entry?.loading, entry?.revision])
  const files = useMemo(() => parsePatch(entry?.patch ?? ''), [entry?.patch])
  return {
    files,
    truncated: entry?.truncated ?? false,
    loading: entry === undefined || (entry.loading && entry.patch === ''),
    error: entry?.error,
    refresh: () => void loadRepoPatch(runID, repo),
  }
}
