import { useEffect, useState } from 'react'
import { ApiError } from '@/lib/api'
import { errorSentence } from '@/lib/format'

export const optimisticDeadlineMs = 5_000

export const stillWaiting = 'Still waiting for the server…'

export interface Mutation<T> {
  apply: () => void
  /** Every call must carry the same idempotency key, or be idempotent by nature. */
  send: () => Promise<T>
  /** Also called for an answer that arrives after the deadline reverted. */
  confirm: (result: T) => void
  revert: () => void
}

export type Outcome<T> =
  | { ok: true; result: T }
  | {
      ok: false
      error: string
      unanswered: boolean
      retry: () => Promise<Outcome<T>>
      /** On an unanswered outcome: resolves with the server's refusal if that request is refused later. */
      refused?: Promise<string>
    }

/** A JSON-RPC error the server sent. The local gateway answers a dead hop with
 * -32004 "server unreachable: ..." or "network unreachable: ...", which says
 * nothing about whether the server committed; its `data.refusal` marks only
 * the edge-path refusals among them, so the prefix stays the test. */
export function refusedByServer(cause: unknown): boolean {
  return cause instanceof ApiError && cause.code !== undefined && !cause.data?.refusal &&
    !/(server|network) unreachable: /.test(cause.message)
}

/** Each mutation's first answered result; once one attempt is answered, no attempt of it reverts. */
const answers = new WeakMap<object, { result: unknown }>()

export function optimistic<T>(mutation: Mutation<T>): Promise<Outcome<T>> {
  const retry = () => optimistic(mutation)
  const answered = () => answers.get(mutation) as { result: T } | undefined
  mutation.apply()
  return new Promise((resolve) => {
    let settled = false
    let refuse = (_: string) => {}
    const timer = setTimeout(() => {
      settled = true
      const answer = answered()
      if (answer) return resolve({ ok: true, result: answer.result })
      mutation.revert()
      resolve({
        ok: false,
        error: `The server did not answer in ${optimisticDeadlineMs / 1000} seconds.`,
        unanswered: true,
        retry,
        refused: new Promise((resolve) => (refuse = resolve)),
      })
    }, optimisticDeadlineMs)
    mutation.send().then(
      (result) => {
        clearTimeout(timer)
        if (!answers.has(mutation)) answers.set(mutation, { result })
        mutation.confirm(result)
        if (settled) return
        settled = true
        resolve({ ok: true, result })
      },
      (cause: unknown) => {
        const refused = refusedByServer(cause)
        if (settled) {
          if (refused && !answered()) refuse(errorSentence(cause))
          return
        }
        settled = true
        clearTimeout(timer)
        const answer = answered()
        if (answer) return resolve({ ok: true, result: answer.result })
        mutation.revert()
        resolve({ ok: false, error: errorSentence(cause), unanswered: !refused, retry })
      },
    )
  })
}

export function usePastDeadline(pending: boolean): boolean {
  const [past, setPast] = useState(false)
  useEffect(() => {
    if (!pending) return
    const timer = setTimeout(() => setPast(true), optimisticDeadlineMs)
    return () => {
      clearTimeout(timer)
      setPast(false)
    }
  }, [pending])
  return pending && past
}
