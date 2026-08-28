import { createRootStore } from '@/store'
import { ENV_EDIT_LINE_CAP } from '@/store/environment'

describe('environment edit slice', () => {
  it('primes an edit before the first event frame can land', () => {
    const store = createRootStore()
    store.getState().startEnvEdit('wsp_1', 'claude')

    expect(store.getState().envEdits.wsp_1).toEqual({
      harness: 'claude',
      status: 'running',
      lines: [],
    })
  })

  it('appends output lines and keeps only the newest within the cap', () => {
    const store = createRootStore()
    store.getState().startEnvEdit('wsp_1', 'claude')
    for (let i = 0; i < ENV_EDIT_LINE_CAP + 5; i++) {
      store.getState().applyEnvEdit('wsp_1', {
        harness: 'claude',
        status: 'running',
        line: `line ${i}`,
      })
    }

    const edit = store.getState().envEdits.wsp_1
    expect(edit.lines).toHaveLength(ENV_EDIT_LINE_CAP)
    expect(edit.lines[0]).toBe('line 5')
    expect(edit.lines[edit.lines.length - 1]).toBe(
      `line ${ENV_EDIT_LINE_CAP + 4}`,
    )
  })

  it('records the proposed version on proposed', () => {
    const store = createRootStore()
    store.getState().applyEnvEdit('wsp_1', {
      harness: 'claude',
      status: 'running',
      line: 'thinking',
    })
    store.getState().applyEnvEdit('wsp_1', {
      harness: 'claude',
      status: 'proposed',
      version: 3,
    })

    expect(store.getState().envEdits.wsp_1).toMatchObject({
      status: 'proposed',
      version: 3,
    })
    // The window survives the terminal frame so the expander stays readable.
    expect(store.getState().envEdits.wsp_1.lines).toEqual(['thinking'])
  })

  it('records the failure detail on failed', () => {
    const store = createRootStore()
    store.getState().applyEnvEdit('wsp_1', {
      harness: 'codex',
      status: 'failed',
      detail: 'codex is not registered: run aether agent add codex --workspace main-repo',
    })

    expect(store.getState().envEdits.wsp_1).toMatchObject({
      harness: 'codex',
      status: 'failed',
      detail:
        'codex is not registered: run aether agent add codex --workspace main-repo',
    })
  })

  it('starts fresh when a new run begins after a terminal state', () => {
    const store = createRootStore()
    store.getState().applyEnvEdit('wsp_1', {
      harness: 'claude',
      status: 'failed',
      detail: 'timed out',
    })
    store.getState().applyEnvEdit('wsp_1', {
      harness: 'pi',
      status: 'running',
      line: 'starting over',
    })

    expect(store.getState().envEdits.wsp_1).toEqual({
      harness: 'pi',
      status: 'running',
      lines: ['starting over'],
    })
  })

  it('clears an edit outright on dismiss', () => {
    const store = createRootStore()
    store.getState().startEnvEdit('wsp_1', 'claude')
    store.getState().clearEnvEdit('wsp_1')

    expect(store.getState().envEdits.wsp_1).toBeUndefined()
  })
})
