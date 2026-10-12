import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { sameIdentity, useStore } from '@/store'
import type { Outcome } from '@/store/optimistic'

interface Failure {
  id: number
  /** Failures with the same key are one action's: starting it again clears its earlier failure. */
  key?: string
  error: string
  retry?: () => void
}

const hosts = new Map<string, ((state: never) => void)[]>()
const waiting = new Map<string, unknown>()

export function useReopenHost<S>(key: string, open: (state: S) => void) {
  const latest = useRef(open)
  useEffect(() => {
    latest.current = open
  })
  useEffect(() => {
    const host = (state: S) => latest.current(state)
    hosts.set(key, [...(hosts.get(key) ?? []), host])
    if (waiting.has(key)) {
      const state = waiting.get(key) as S
      waiting.delete(key)
      host(state)
    }
    return () => void hosts.set(key, (hosts.get(key) ?? []).filter((h) => h !== host))
  }, [key])
}

export function reopen<S>(key: string, state: S, route?: string) {
  const host = hosts.get(key)?.at(-1) as ((state: S) => void) | undefined
  if (host) return host(state)
  waiting.set(key, state)
  if (route) useStore.getState().navigate(route)
}

const draftKeys = new WeakMap<object, number>()
let drafts = 0

/** A form keyed by this remounts when a reopened draft replaces the one it shows. */
export function draftKey(draft?: object): number {
  if (!draft) return 0
  if (!draftKeys.has(draft)) draftKeys.set(draft, ++drafts)
  return draftKeys.get(draft)!
}

const identityToasts = new Map<string | number, () => boolean>()

useStore.subscribe((state, previous) => {
  if (state.identityKey === previous.identityKey) return
  identityToasts.forEach((same, id) => {
    if (same()) return
    toast.dismiss(id)
    identityToasts.delete(id)
  })
})

/** Dismisses toast `id` once `same`, from `sameIdentity()` when its action started, turns false. */
export function dismissOnIdentityChange(id: string | number, same: () => boolean) {
  identityToasts.set(id, same)
}

/**
 * Shows the failure of an action whose view has gone: the error verbatim, Retry when the server did not answer, Reopen to put the draft back.
 * `same`, from `sameIdentity()` when the action started, dismisses the toast once the signed-in identity changes.
 */
export function toastFailure(
  error: string,
  { id, retry, reopen, same }: { id?: string | number; retry?: () => void; reopen?: () => void; same?: () => boolean },
) {
  const shown = toast.error(error, {
    id,
    action: retry ? { label: 'Retry', onClick: retry } : undefined,
    cancel: reopen ? { label: 'Reopen', onClick: reopen } : undefined,
  })
  if (same) dismissOnIdentityChange(shown, same)
  return shown
}

/**
 * Offers Retry for an action the server did not answer; use it only where a resend cannot repeat the effect.
 * `attempt` receives `answered`, which its `confirm` must call so a late success clears that action's failure only.
 * Once the component has unmounted, a failure shows as a toast instead, with `reopen` as its Reopen action.
 * After the signed-in identity changes, the action's failure, Retry and Reopen do nothing and its toast is dismissed.
 */
export function useFailure() {
  const [failures, setFailures] = useState<Failure[]>([])
  const ids = useRef(0)
  const mounted = useRef(true)
  const toasts = useRef(new Map<number, string | number>())
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const show = (id: number, same: () => boolean, failure?: Omit<Failure, 'id'>, reopen?: (error: string) => void) => {
    const shown = toasts.current.get(id)
    if ((!failure || !same()) && shown !== undefined) toast.dismiss(shown)
    if (failure && !same()) return
    if (failure && !mounted.current) {
      const again = reopen && (() => same() && reopen(failure.error))
      toasts.current.set(id, toastFailure(failure.error, { id: shown, retry: failure.retry, reopen: again, same }))
      return
    }
    setFailures((list) => [...list.filter((f) => f.id !== id), ...(failure ? [{ id, ...failure }] : [])])
  }
  const run = async <T>(
    attempt: (answered: () => void) => Promise<Outcome<T>>,
    { onDone, key, reopen }: { onDone?: (result: T) => void; key?: string; reopen?: (error: string) => void } = {},
    id = ++ids.current,
    same = sameIdentity(),
  ): Promise<void> => {
    setFailures((list) => list.filter((f) => f.id !== id && (key === undefined || f.key !== key)))
    const outcome = await attempt(() => show(id, same))
    if (outcome.ok) {
      onDone?.(outcome.result)
      return
    }
    const retry = outcome.unanswered ? () => void (same() && run(outcome.retry, { onDone, key, reopen }, id, same)) : undefined
    show(id, same, { key, error: outcome.error, retry }, reopen)
    void outcome.refused?.then((error) => show(id, same, { key, error }, reopen))
  }
  return { failures, run }
}
