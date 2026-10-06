const running = new WeakMap<object, Map<string, { again: boolean }>>()

/**
 * At most one read in flight and one queued per owner and key. `read` must
 * not reject; resolving false asks for it to run again.
 */
export function coalesce(owner: object, key: string, read: () => Promise<boolean | void>): void {
  let reads = running.get(owner)
  if (!reads) {
    reads = new Map()
    running.set(owner, reads)
  }
  const current = reads.get(key)
  if (current) {
    current.again = true
    return
  }
  const entry = { again: true }
  reads.set(key, entry)
  void (async () => {
    try {
      while (entry.again) {
        entry.again = false
        if ((await read()) === false) entry.again = true
      }
    } finally {
      reads.delete(key)
    }
  })()
}

/** Backoff before retrying a failed read: 5s doubling, capped at a minute. */
export function readRetryDelay(attempt: number): number {
  return Math.min(5000 * 2 ** attempt, 60_000)
}
