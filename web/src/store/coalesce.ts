/** Owner to the reads it has running, by key. */
const running = new WeakMap<object, Map<string, { again: boolean }>>()

/**
 * Starts `read` for `key` without waiting on it, at most one in flight and
 * one queued per owner: a burst of events asking for the same read costs
 * two requests, not one each. `read` must not reject; resolving false asks
 * for it to run again.
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
