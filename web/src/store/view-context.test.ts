import { createRootStore } from '@/store'
import { buildViewContext, createViewHistory, maxRecent, maxViewContextBytes, trackViewContext } from '@/store/view-context'

describe('the view history', () => {
  it('keeps one entry per page, newest first, at most maxRecent, and forgets them when stopped', () => {
    const store = createRootStore()
    const history = createViewHistory()
    const stop = trackViewContext(store, history)
    store.getState().navigate('run', { runId: 'run-a', view: 'session' })
    store.getState().navigate('board')
    store.getState().navigate('run', { runId: 'run-a', view: 'changes' })
    expect(history.list().map((e) => [e.name, e.run_id, e.view])).toEqual([['run', 'run-a', 'changes'], ['board', undefined, undefined]])

    for (let i = 0; i < 20; i++) store.getState().navigate('run', { runId: `run-${i}` })
    expect(history.list()).toHaveLength(maxRecent)
    expect(history.list()[0]!.run_id).toBe('run-19')

    const context = buildViewContext(store.getState(), history)
    expect(context.route).toMatchObject({ name: 'run', run_id: 'run-19' })
    expect(context.recent?.some((e) => e.run_id === 'run-19')).toBe(false)
    stop()
    expect(history.list()).toEqual([])
  })

  it('drops the oldest pages to stay within the size limit', () => {
    const store = createRootStore()
    const history = createViewHistory()
    for (let i = 0; i < maxRecent; i++) history.note({ name: 'run', run_id: `run-${i}`, subagent: 'x'.repeat(600) }, new Date())
    const context = buildViewContext(store.getState(), history)
    expect(JSON.stringify(context).length).toBeLessThanOrEqual(maxViewContextBytes)
    expect(context.recent!.length).toBeLessThan(maxRecent)
    expect(context.recent![0]!.run_id).toBe(`run-${maxRecent - 1}`)
  })
})
