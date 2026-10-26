import { useEffect } from 'react'
import { create } from 'zustand'
import { api, type Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { ChildSettings, ChildSettingsParams } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

/** The viewer's child-run setting, which Settings and every nudge share; null until read. */
export const useChildSettings = create<{ settings: ChildSettings | null; error: string | null }>(() => ({ settings: null, error: null }))

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useChildSettings.setState({ settings: null, error: null }, true)
})

export function saveChildSettings(params: ChildSettingsParams, client: Api = api): Promise<ChildSettings> {
  const same = sameIdentity()
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
    client.childSettings().then(
      (read) => same() && useChildSettings.setState({ settings: read, error: null }),
      (err: unknown) => same() && useChildSettings.setState({ error: errorSentence(err) }),
    )
  }, [client, readable, identity, attempt])
  return readable ? settings : null
}

/** Stopping `run` stops these too: its direct child runs that are still going and not detached. */
export function unfinishedChildren(runs: Record<string, RunRecord>, run: RunRecord): RunRecord[] {
  return Object.values(runs).filter((child) =>
    child.parent_run_id === run.id && child.mission_role !== 'worker' && !child.detached && !isTerminal(child.status))
}
