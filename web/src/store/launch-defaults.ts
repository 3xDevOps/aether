import { useEffect } from 'react'
import { create } from 'zustand'
import { api, ApiError, type Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { LaunchDefaultIssue, LaunchDefaultsResult, LaunchMode, SavedLaunchDefaults } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'
import { useCapability } from '@/store/hooks'

const codeConflict = -32003

/** The viewer's saved launch choices on the server, which the launch dialog and Settings share; null until read. */
export const useLaunchDefaults = create<{ result: LaunchDefaultsResult | null; error: string | null }>(() => ({ result: null, error: null }))

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useLaunchDefaults.setState({ result: null, error: null }, true)
})

// Every save bumps writes, so a read that started before it never replaces
// what the save stored; one read is shared by every caller.
let writes = 0
let pending: { identity: string | null; read: Promise<LaunchDefaultsResult> } | null = null

/** Whether this gateway keeps saved launch choices; without it the dialog uses this browser's copy. */
export function useLaunchDefaultsSupported(): boolean {
  return useCapability().hasMethod('launch.defaults.get')
}

/** Reads the saved choices once per identity, or again after `attempt` changes. */
export function useLaunchDefaultsRead(client: Api = api, attempt = 0): { result: LaunchDefaultsResult | null; error: string | null } {
  const readable = useLaunchDefaultsSupported()
  const identity = useStore((s) => s.identityKey)
  const result = useLaunchDefaults((s) => s.result)
  const error = useLaunchDefaults((s) => s.error)
  useEffect(() => {
    if (!readable || useLaunchDefaults.getState().result) return
    const same = sameIdentity()
    const before = writes
    let shared = pending?.identity === identity ? pending.read : null
    if (!shared) {
      const read = client.launchDefaultsGet().finally(() => { if (pending?.read === read) pending = null })
      pending = { identity, read }
      shared = read
    }
    shared.then(
      (read) => same() && writes === before && useLaunchDefaults.setState({ result: read, error: null }),
      (err: unknown) => same() && useLaunchDefaults.setState({ error: errorSentence(err) }),
    )
  }, [client, readable, identity, attempt])
  return readable ? { result, error } : { result: null, error: null }
}

/**
 * Saves `change` applied to the saved choices; null clears them. When another
 * device saved since they were read, the server's refusal carries its current
 * choices, and `change` is applied to those and saved once more.
 */
export async function saveLaunchDefaults(change: (current: SavedLaunchDefaults) => SavedLaunchDefaults | null, client: Api = api): Promise<LaunchDefaultsResult> {
  const same = sameIdentity()
  writes++
  const save = (base: LaunchDefaultsResult) => {
    const next = change(base.defaults)
    return client.launchDefaultsSet(next ? { defaults: next, revision: base.revision } : { clear: true, revision: base.revision })
  }
  const current = useLaunchDefaults.getState().result ?? await client.launchDefaultsGet()
  let result: LaunchDefaultsResult
  try {
    result = await save(current)
  } catch (err) {
    if (!(err instanceof ApiError && err.code === codeConflict && err.data)) throw err
    result = await save(err.data as unknown as LaunchDefaultsResult)
  }
  if (same()) useLaunchDefaults.setState({ result, error: null })
  return result
}

/** `defaults` with `agent` preferred and its mode and model replaced; an empty model forgets the saved one. */
export function withAgentChoice(defaults: SavedLaunchDefaults, agent: string, mode: LaunchMode, model: string): SavedLaunchDefaults {
  return { ...defaults, agent, agents: { ...defaults.agents, [agent]: { mode, ...(model ? { model } : {}) } } }
}

/** The saved choices the server found unusable, by field. */
export function invalidFields(result: LaunchDefaultsResult | null): Map<string, LaunchDefaultIssue> {
  return new Map((result?.invalid ?? []).map((issue) => [issue.field, issue]))
}
