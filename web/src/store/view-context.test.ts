import { ApiError } from '@/lib/api'
import { createRootStore } from '@/store'
import type { ViewContext } from '@/lib/assistant-types'
import { draftViewContext, maxRecent, maxViewContextBytes, saveShareView, shareRetryDelay, syncedViewContext, syncViewContext, trackViewContext, viewSyncDelay } from '@/store/view-context'

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
    const context = syncedViewContext(store.getState())!
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
    expect(syncedViewContext(store.getState())!.open_file).toEqual({ kind: 'workspace', path, run_id: 'run-a', workspace_id: 'ws' })
    store.getState().setShownRunView('run-a', 'changes')
    expect(syncedViewContext(store.getState())!.open_file).toBeUndefined()
    store.getState().navigate('run', { runId: 'run-b', view: 'files' })
    expect(syncedViewContext(store.getState())!.open_file).toBeUndefined()

    for (let i = 0; i < maxRecent; i++) store.getState().noteView({ name: 'run', run_id: `run-${i}`, subagent: 'ü'.repeat(300) }, new Date())
    const context = syncedViewContext(store.getState())!
    expect(new TextEncoder().encode(JSON.stringify(context)).length).toBeLessThanOrEqual(maxViewContextBytes)
    expect(context.recent!.length).toBeLessThan(maxRecent)
    expect(context.recent![0]!.run_id).toBe(`run-${maxRecent - 1}`)
  })

  it('counts bytes as the server encodes them, which escapes < > and &', () => {
    const store = createRootStore()
    const check = `${'&'.repeat(127)}1`
    store.getState().askAssistant({ draft: 'fix these', attachments: Array.from({ length: 6 }, (_, i) => ({ kind: 'check' as const, run_id: `run-${i}`, check, label: check })) })
    const context = draftViewContext(store.getState())!
    expect(new TextEncoder().encode(JSON.stringify({ ...context, attachments: store.getState().assistantAttachments.map(({ label: _, ...a }) => a) })).length).toBeLessThan(maxViewContextBytes)
    expect(context.attachments!.length).toBeLessThan(6)
  })
})

describe('the page a draft was started on', () => {
  it('follows the member\'s later views of that page, so "where you started" sends the last one', () => {
    const store = createRootStore()
    store.getState().setAssistantShareChoice(true)
    const stop = trackViewContext(store)
    store.getState().navigate('run', { runId: 'run-a' })
    store.getState().setShownRunView('run-a', 'session')
    store.getState().setAssistantDraft('why?')
    store.getState().openFileTab({ key: 'f', kind: 'workspace', workspaceID: 'ws', runID: 'run-a', path: 'src/app.ts', label: 'app.ts' })
    store.getState().navigate('run', { runId: 'run-a', view: 'files' })
    store.getState().setShownRunView('run-a', 'files')
    store.getState().navigate('run', { runId: 'run-b' })
    store.getState().chooseAssistantPage(false)
    const context = draftViewContext(store.getState())
    expect(context?.route).toMatchObject({ run_id: 'run-a', view: 'files' })
    expect(context?.open_file).toMatchObject({ path: 'src/app.ts', run_id: 'run-a' })
    stop()
  })
})

describe('saving the sharing choice', () => {
  afterEach(() => vi.useRealTimers())

  it('retries with backoff for the member who chose, and stops once another signs in or the server refuses', async () => {
    vi.useFakeTimers()
    const store = createRootStore()
    store.getState().setIdentityKey('ada')
    const assistantSettings = vi.fn(async () => { throw new ApiError(503, 'assistant.settings: ssh: connection lost') })
    saveShareView(store, { assistantSettings }, false, () => {})
    await vi.advanceTimersByTimeAsync(shareRetryDelay)
    expect(assistantSettings).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(shareRetryDelay)
    expect(assistantSettings).toHaveBeenCalledTimes(2)

    store.getState().resetAssistant()
    store.getState().setIdentityKey('bob')
    store.getState().setAssistantShareChoice(false)
    await vi.advanceTimersByTimeAsync(10 * shareRetryDelay)
    expect(assistantSettings).toHaveBeenCalledTimes(2)

    assistantSettings.mockRejectedValue(new ApiError(200, 'assistant.settings: invalid', -32602))
    saveShareView(store, { assistantSettings }, true, () => {})
    await vi.advanceTimersByTimeAsync(10 * shareRetryDelay)
    expect(assistantSettings).toHaveBeenCalledTimes(3)
    expect(store.getState().assistantShareChoice).toBeNull()
  })

  it('never lets an older choice land after a newer one', async () => {
    vi.useFakeTimers()
    const store = createRootStore()
    let fail!: (err: unknown) => void
    const assistantSettings = vi.fn((params?: { share_view?: boolean }) =>
      params?.share_view ? new Promise<never>((_, reject) => { fail = reject }) : Promise.resolve({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: false }))
    saveShareView(store, { assistantSettings }, true, () => {})
    await vi.advanceTimersByTimeAsync(0)
    saveShareView(store, { assistantSettings }, false, () => {})
    await vi.advanceTimersByTimeAsync(0)
    expect(assistantSettings.mock.calls.map(([p]) => p?.share_view)).toEqual([true])
    fail(new ApiError(503, 'assistant.settings: ssh: connection lost'))
    await vi.advanceTimersByTimeAsync(10 * shareRetryDelay)
    expect(assistantSettings.mock.calls.map(([p]) => p?.share_view)).toEqual([true, false])
    expect(store.getState().assistantShareChoice).toBeNull()
  })
})

describe('syncing the page between prompts', () => {
  afterEach(() => vi.useRealTimers())

  it('lets the agent read only the page the chips show', async () => {
    vi.useFakeTimers()
    const store = createRootStore()
    store.getState().setAssistantShareChoice(true)
    store.getState().navigate('run', { runId: 'run-a' })
    const assistantView = vi.fn(async (_: ViewContext) => ({}))
    const stop = syncViewContext(store, { assistantView })
    const last = () => assistantView.mock.lastCall?.[0]
    await vi.advanceTimersByTimeAsync(viewSyncDelay)
    expect(last()?.route).toMatchObject({ run_id: 'run-a' })

    store.getState().setAssistantDraft('why?')
    store.getState().setAssistantPageRemoved(true)
    await vi.advanceTimersByTimeAsync(viewSyncDelay)
    expect(last()?.route).toBeUndefined()
    store.getState().navigate('run', { runId: 'run-b' })
    await vi.advanceTimersByTimeAsync(viewSyncDelay)
    expect(last()?.route).toBeUndefined()

    store.getState().navigate('run', { runId: 'run-a' })
    store.getState().setAssistantPageRemoved(false)
    await vi.advanceTimersByTimeAsync(viewSyncDelay)
    expect(last()?.route).toMatchObject({ run_id: 'run-a' })
    const calls = assistantView.mock.calls.length
    store.getState().navigate('run', { runId: 'run-b' })
    await vi.advanceTimersByTimeAsync(viewSyncDelay)
    expect(assistantView).toHaveBeenCalledTimes(calls)
    store.getState().chooseAssistantPage(false)
    await vi.advanceTimersByTimeAsync(viewSyncDelay)
    expect(assistantView).toHaveBeenCalledTimes(calls)

    store.getState().clearAssistantDraft()
    await vi.advanceTimersByTimeAsync(viewSyncDelay)
    expect(last()?.route).toMatchObject({ run_id: 'run-b' })
    stop()
  })
})
