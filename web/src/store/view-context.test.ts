import { createRootStore } from '@/store'
import { buildViewContext, maxRecent, maxViewContextBytes, trackViewContext } from '@/store/view-context'

describe('the view context', () => {
  it('keeps one entry per page with the view it showed, newest first, at most maxRecent, and forgets them when stopped', () => {
    const store = createRootStore()
    const stop = trackViewContext(store)
    store.getState().navigate('run', { runId: 'run-a' })
    store.getState().setShownRunView('run-a', 'session')
    store.getState().navigate('board')
    store.getState().navigate('run', { runId: 'run-a' })
    store.getState().setShownRunView('run-a', 'changes')
    expect(store.getState().viewHistory.map((e) => [e.name, e.run_id, e.view])).toEqual([['run', 'run-a', 'changes'], ['board', undefined, undefined]])

    for (let i = 0; i < 20; i++) store.getState().navigate('run', { runId: `run-${i}` })
    expect(store.getState().viewHistory).toHaveLength(maxRecent)
    const context = buildViewContext(store.getState())
    expect(context.route).toMatchObject({ name: 'run', run_id: 'run-19' })
    expect(context.recent?.some((e) => e.run_id === 'run-19')).toBe(false)
    stop()
    expect(store.getState().viewHistory).toEqual([])
  })

  it('names the open file only where it is on screen, and trims the oldest pages to the byte budget', () => {
    const store = createRootStore()
    const path = 'ü'.repeat(200)
    store.getState().openFileTab({ key: 'f', kind: 'workspace', workspaceID: 'ws', runID: 'run-a', path, label: 'a' })
    store.getState().navigate('run', { runId: 'run-a', view: 'files' })
    store.getState().setShownRunView('run-a', 'files')
    expect(buildViewContext(store.getState()).open_file).toEqual({ kind: 'workspace', path, run_id: 'run-a', workspace_id: 'ws' })
    store.getState().setShownRunView('run-a', 'changes')
    expect(buildViewContext(store.getState()).open_file).toBeUndefined()
    store.getState().navigate('run', { runId: 'run-b', view: 'files' })
    expect(buildViewContext(store.getState()).open_file).toBeUndefined()

    for (let i = 0; i < maxRecent; i++) store.getState().noteView({ name: 'run', run_id: `run-${i}`, subagent: 'ü'.repeat(300) }, new Date())
    const context = buildViewContext(store.getState())
    expect(new TextEncoder().encode(JSON.stringify(context)).length).toBeLessThanOrEqual(maxViewContextBytes)
    expect(context.recent!.length).toBeLessThan(maxRecent)
    expect(context.recent![0]!.run_id).toBe(`run-${maxRecent - 1}`)
  })
})
