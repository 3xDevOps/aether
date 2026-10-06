import type { Event } from '@/lib/types'
import { createRootStore } from '@/store'
import { applyEvent } from '@/store/sync'
import { fakeApi, run, workspace } from '@/test/fixtures'

function agentEvent(seq: number, payload: Record<string, unknown>, runID = 'run_1'): Event {
  return {
    id: `evt_${seq}`,
    seq,
    time: `2026-08-14T11:00:0${seq}Z`,
    workspace_id: workspace.id,
    run_id: runID,
    actor_id: '',
    type: 'run.agent',
    payload,
  }
}

describe('run activity', () => {
  it('follows run.agent events from call to result', async () => {
    const store = createRootStore()
    store.setState({ workspaces: { [workspace.id]: workspace } })
    store.getState().setRuns([run()])
    const client = fakeApi()
    const activity = () => store.getState().runs.run_1.activity

    await applyEvent(store, agentEvent(1, { kind: 'session', harness_session_id: 's' }), client)
    expect(activity()).toBeUndefined()

    await applyEvent(store, agentEvent(2, { kind: 'tool_call', tool: 'Read', detail: 'src/auth.ts' }), client)
    expect(activity()).toEqual({ verb: 'Reading', target: 'src/auth.ts', at: '2026-08-14T11:00:02Z' })

    await applyEvent(store, agentEvent(3, { kind: 'tool_result', tool_use_id: 'x' }), client)
    expect(activity()).toEqual({ verb: 'Read', target: 'src/auth.ts', at: '2026-08-14T11:00:03Z' })

    await applyEvent(store, agentEvent(4, { kind: 'tool_call', tool: 'Bash', detail: 'go test ./...' }), client)
    await applyEvent(store, agentEvent(5, { kind: 'tool_result', is_error: true }), client)
    expect(activity()).toMatchObject({ verb: 'Failed', target: 'go test ./...' })

    await applyEvent(store, agentEvent(6, { kind: 'subagent', tool: 'Task', detail: 'review the diff' }), client)
    expect(activity()).toMatchObject({ verb: 'Delegating', target: 'review the diff' })

    await applyEvent(store, agentEvent(7, { kind: 'tool_call', tool: 'mcp_lookup' }), client)
    await applyEvent(store, agentEvent(8, { kind: 'tool_result' }), client)
    expect(activity()).toMatchObject({ verb: 'Used mcp_lookup', target: 'mcp_lookup' })

    // A snapshot re-read keeps what no snapshot carries.
    store.getState().upsertRun(run({ title: 'renamed' }))
    expect(activity()).toMatchObject({ verb: 'Used mcp_lookup' })
    expect(store.getState().lastSeq).toBe(8)
  })

  it('ends the call a result names when tool calls interleave', async () => {
    const store = createRootStore()
    store.setState({ workspaces: { [workspace.id]: workspace } })
    store.getState().setRuns([run()])
    const client = fakeApi()
    const activity = () => store.getState().runs.run_1.activity

    await applyEvent(store, agentEvent(1, { kind: 'tool_call', tool: 'Read', tool_use_id: 'a', detail: 'src/auth.ts' }), client)
    await applyEvent(store, agentEvent(2, { kind: 'tool_call', tool: 'Bash', tool_use_id: 'b', detail: 'go test ./...' }), client)
    expect(activity()).toMatchObject({ verb: 'Running', target: 'go test ./...' })

    await applyEvent(store, agentEvent(3, { kind: 'tool_result', tool_use_id: 'a' }), client)
    expect(activity()).toMatchObject({ verb: 'Running', target: 'go test ./...' })

    await applyEvent(store, agentEvent(4, { kind: 'tool_result', tool_use_id: 'b', is_error: true }), client)
    expect(activity()).toMatchObject({ verb: 'Failed', target: 'go test ./...', running: undefined })

    await applyEvent(store, agentEvent(5, { kind: 'tool_call', tool: 'Bash', tool_use_id: 'c', detail: 'make lint' }), client)
    await applyEvent(store, agentEvent(6, { kind: 'tool_call', tool: 'Read', tool_use_id: 'd', detail: 'go.mod' }), client)
    await applyEvent(store, agentEvent(7, { kind: 'tool_result', tool_use_id: 'd' }), client)
    expect(activity()).toMatchObject({ verb: 'Running', target: 'make lint' })
    await applyEvent(store, agentEvent(8, { kind: 'tool_result', tool_use_id: 'c' }), client)
    expect(activity()).toMatchObject({ verb: 'Ran', target: 'make lint' })
  })

  it('drops the last action when the run changes status', async () => {
    const store = createRootStore()
    store.setState({ workspaces: { [workspace.id]: workspace } })
    store.getState().setRuns([run()])
    const client = fakeApi()
    const activity = () => store.getState().runs.run_1.activity
    const status = (seq: number, to: string): Event => ({
      ...agentEvent(seq, { from: 'running', to }),
      type: 'run.status',
    })

    await applyEvent(store, agentEvent(1, { kind: 'tool_call', tool: 'Read', detail: 'src/auth.ts' }), client)
    await applyEvent(store, status(2, 'needs-attention'), client)
    expect(activity()).toBeUndefined()

    await applyEvent(store, agentEvent(3, { kind: 'tool_call', tool: 'Bash', detail: 'make lint' }), client)
    await applyEvent(store, status(4, 'running'), client)
    expect(activity()).toBeUndefined()

    await applyEvent(store, agentEvent(5, { kind: 'tool_call', tool: 'Read', detail: 'go.mod' }), client)
    store.getState().setRuns([run()])
    expect(activity()).toMatchObject({ verb: 'Reading', target: 'go.mod' })
    store.getState().setRuns([run({ status: 'provisioning' })])
    expect(activity()).toBeUndefined()
  })

  it('ignores activity for a run the client has not loaded', async () => {
    const store = createRootStore()
    store.setState({ workspaces: { [workspace.id]: workspace } })
    const runGet = vi.fn()
    expect(await applyEvent(store, agentEvent(1, { kind: 'tool_call', tool: 'Read' }, 'run_unknown'), fakeApi({ runGet }))).toBe(true)
    expect(runGet).not.toHaveBeenCalled()
    expect(store.getState().runs.run_unknown).toBeUndefined()
  })
})
