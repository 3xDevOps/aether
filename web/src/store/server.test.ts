import { describe, expect, it } from 'vitest'
import { createRootStore } from '@/store'

describe('server terminal cache epoch', () => {
  it('clears the event cursor and advances on an event-log reset', () => {
    const store = createRootStore()
    store.setState({ lastSeq: 42 })

    expect(store.getState().terminalCacheEpoch).toBe(0)

    store.getState().resetSeq()

    expect(store.getState().lastSeq).toBe(0)
    expect(store.getState().terminalCacheEpoch).toBe(1)
  })

  it('stays unchanged across ordinary connection resets and reconnects', () => {
    const store = createRootStore()
    store.setState({ lastSeq: 42, terminalCacheEpoch: 7 })

    store.getState().resetConnection()
    expect(store.getState().terminalCacheEpoch).toBe(7)

    store.getState().reconnect()
    expect(store.getState().terminalCacheEpoch).toBe(7)
  })
})
