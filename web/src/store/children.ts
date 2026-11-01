import { useEffect } from 'react'
import { create } from 'zustand'
import { api, type Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { ChildSettings, ChildSettingsParams } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

/** The viewer's child-run setting, which Settings and the launch dialog share; null until read. */
export const useChildSettings = create<{ settings: ChildSettings | null; error: string | null }>(() => ({ settings: null, error: null }))

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useChildSettings.setState({ settings: null, error: null }, true)
})

// Every save bumps writes, so a read that started before it never replaces
// what the save stored; one read is shared by every caller.
let writes = 0
let pending: { identity: string | null; read: Promise<ChildSettings> } | null = null

export function saveChildSettings(params: ChildSettingsParams, client: Api = api): Promise<ChildSettings> {
  const same = sameIdentity()
  writes++
  return client.childSettings(params).then((settings) => {
    if (same()) useChildSettings.setState({ settings, error: null })
    return settings
  })
}

/** Reads the setting once per identity, or again after `attempt` changes. */
export function useChildSettingsRead(client: Api = api, attempt = 0): ChildSettings | null {
  const readable = useCapability().hasMethod('child.settings')
  const identity = useStore((s) => s.identityKey)
  const settings = useChildSettings((s) => s.settings)
  useEffect(() => {
    if (!readable || useChildSettings.getState().settings) return
    const same = sameIdentity()
    const before = writes
    let shared = pending?.identity === identity ? pending.read : null
    if (!shared) {
      const read = client.childSettings().finally(() => { if (pending?.read === read) pending = null })
      pending = { identity, read }
      shared = read
    }
    shared.then(
      (read) => same() && writes === before && useChildSettings.setState({ settings: read, error: null }),
      (err: unknown) => same() && useChildSettings.setState({ error: errorSentence(err) }),
    )
  }, [client, readable, identity, attempt])
  return readable ? settings : null
}

/** Stopping `run` stops these too: its direct child runs that are still going and not detached. */
export function unfinishedChildren(runs: Record<string, RunRecord>, run: RunRecord): RunRecord[] {
  return Object.values(runs).filter((child) =>
    child.parent_run_id === run.id && !child.detached && !isTerminal(child.status))
}
