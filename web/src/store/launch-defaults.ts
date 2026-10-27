import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { create } from 'zustand'
import { api, ApiError, type Api } from '@/lib/api'
import { errorSentence, message } from '@/lib/format'
import type { LaunchDefaultsResult, LaunchMode, SavedLaunchDefaults } from '@/lib/types'
import { sameIdentity, useStore } from '@/store'
import { capability, useCapability } from '@/store/hooks'

const codeConflict = -32003

/** The viewer's saved launch choices on the server, which the launch dialog and Settings share; null until read. */
export const useLaunchDefaults = create<{ result: LaunchDefaultsResult | null; error: string | null }>(() => ({ result: null, error: null }))

useStore.subscribe((state, previous) => {
  if (state.identityKey !== previous.identityKey) useLaunchDefaults.setState({ result: null, error: null }, true)
})

// Every read, and every save as it starts and as it stores its answer,
// bumps changes, so the answer to a read that overlapped a later read or a
// save never replaces what that one stored.
let changes = 0
// Saves run one at a time, each from the choices the one before stored, so
// an older answer never replaces a newer one.
let saving: Promise<unknown> = Promise.resolve()

function read(client: Api): Promise<void> {
  const same = sameIdentity()
  const mine = ++changes
  return client.launchDefaultsGet().then(
    (result) => { if (same() && changes === mine) useLaunchDefaults.setState({ result, error: null }) },
    (err: unknown) => { if (same() && changes === mine) useLaunchDefaults.setState({ error: errorSentence(err) }) },
  )
}

/** The mode and model last chosen for `name`: saved on the server when it keeps them, else on this browser. */
export function rememberedChoice(name: string, saved: LaunchDefaultsResult | null): { mode?: LaunchMode; model?: string } {
  if (!saved) return useStore.getState().launchDefaults[name] ?? {}
  const choice = saved.defaults.agents?.[name]
  return { ...(choice?.mode ? { mode: choice.mode } : {}), ...(choice?.model ? { model: choice.model } : {}) }
}

export interface LaunchDefaultsRead { result: LaunchDefaultsResult | null; error: string | null; fresh: boolean }

/**
 * Reads the saved choices on mount, again after `attempt` changes, and when
 * the signed-in member changes. `fresh` turns true once this mount's read
 * settles, since another device or the assistant may have saved since the
 * cached copy was read.
 */
export function useLaunchDefaultsRead(client: Api = api, attempt = 0): LaunchDefaultsRead {
  const readable = useCapability().hasMethod('launch.defaults.get')
  const identity = useStore((s) => s.identityKey)
  const result = useLaunchDefaults((s) => s.result)
  const error = useLaunchDefaults((s) => s.error)
  const [fresh, setFresh] = useState(false)
  useEffect(() => {
    if (!readable) return
    let live = true
    setFresh(false)
    void read(client).finally(() => { if (live) setFresh(true) })
    return () => { live = false }
  }, [client, readable, identity, attempt])
  return readable ? { result, error, fresh } : { result: null, error: null, fresh: true }
}

/**
 * Saves `change` applied to the saved choices; null clears them. When another
 * device saved since they were read, the server's refusal carries its current
 * choices, and `change` is applied to those and saved once more. A failed
 * save reads the choices again, so the shared copy is the server's.
 */
export function saveLaunchDefaults(change: (current: SavedLaunchDefaults) => SavedLaunchDefaults | null, client: Api = api): Promise<LaunchDefaultsResult> {
  const same = sameIdentity()
  const run = async () => {
    changes++
    const save = (base: LaunchDefaultsResult) => {
      if (!same()) throw new Error('Launch choices not saved: the signed-in member changed.')
      const next = change(base.defaults)
      if (same() && useLaunchDefaults.getState().result) useLaunchDefaults.setState({ result: { ...base, defaults: next ?? {} } })
      return client.launchDefaultsSet(next ? { defaults: next, revision: base.revision } : { clear: true, revision: base.revision })
    }
    try {
      const current = useLaunchDefaults.getState().result ?? await client.launchDefaultsGet()
      let result: LaunchDefaultsResult
      try {
        result = await save(current)
      } catch (err) {
        if (!(err instanceof ApiError && err.code === codeConflict && err.data)) throw err
        result = await save(err.data as unknown as LaunchDefaultsResult)
      }
      changes++
      if (same()) useLaunchDefaults.setState({ result, error: null })
      return result
    } catch (err) {
      if (same()) await read(client)
      throw err
    }
  }
  const next = saving.then(run, run)
  saving = next.catch(() => {})
  return next
}

/** `agent`'s mode and model replaced, and `agent` preselected when `preferred`; an empty model forgets the saved one and undefined keeps it. */
export function withAgentChoice(defaults: SavedLaunchDefaults, agent: string, mode: LaunchMode, model?: string, preferred = true): SavedLaunchDefaults {
  const kept = model ?? defaults.agents?.[agent]?.model
  return {
    ...defaults,
    ...(preferred ? { agent } : {}),
    agents: { ...defaults.agents, [agent]: { mode, ...(kept ? { model: kept } : {}) } },
  }
}

/**
 * Records a launch choice the member made outside the launch dialog, such as
 * Run or Default mode on Agents, on this browser and, where the server keeps
 * them, in the saved choices every device starts from.
 */
export function rememberChoice(agent: string, mode: LaunchMode, model?: string, preferred = true): void {
  const state = useStore.getState()
  if (preferred) state.rememberLaunch(agent, mode, model)
  else state.setLaunchDefault(agent, mode)
  if (!capability(state.capabilities).hasMethod('launch.defaults.set')) return
  saveLaunchDefaults((current) => withAgentChoice(current, agent, mode, model, preferred))
    .catch((err) => toast.error(`Launch choices not saved: ${message(err)}`))
}
