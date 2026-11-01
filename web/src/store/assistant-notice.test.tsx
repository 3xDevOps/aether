import { act, cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toaster } from 'sonner'
import { AssistantButton } from '@/components/assistant/button'
import type { AssistantNoticePayload, Event } from '@/lib/types'
import { useStore } from '@/store'
import { applyEvent, hydrate } from '@/store/sync'
import { alice, fakeApi, serverInfo, workspace } from '@/test/fixtures'

const now = new Date('2026-10-15T12:00:00Z')

function notice(over: Partial<AssistantNoticePayload> = {}, ev: Partial<Event> = {}): Event {
  const payload: AssistantNoticePayload = {
    member_id: alice.id,
    run_id: 'run_9',
    kind: 'finished',
    title: 'Fix login finished',
    body: 'Tests pass.',
    ...over,
  }
  return {
    id: 'evt_n',
    seq: 7,
    time: now.toISOString(),
    workspace_id: workspace.id,
    run_id: payload.run_id,
    actor_id: '',
    type: 'assistant.notice',
    payload,
    ...ev,
  }
}

async function deliver(ev: Event) {
  await act(async () => {
    await applyEvent(useStore, ev, fakeApi())
  })
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(now)
  localStorage.clear()
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice },
    workspaces: { [workspace.id]: workspace },
    hydrated: true,
    route: { name: 'board', params: {} },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events'] },
  }, true)
  render(<><Toaster /><AssistantButton /></>)
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('assistant.notice', () => {
  it('toasts the member it names, with an Open run action that opens the run', async () => {
    await deliver(notice())
    expect(await screen.findByText('Fix login finished')).toBeTruthy()
    expect(screen.getByText('Tests pass.')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Open session' }))
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_9' } })
  })

  it('uses the warning style for needs_* kinds only', async () => {
    await deliver(notice({ kind: 'needs_input', title: 'Needs you' }))
    await deliver(notice({ kind: 'finished', title: 'Done' }, { seq: 8 }))
    expect((await screen.findByText('Needs you')).closest('[data-sonner-toast]')?.getAttribute('data-type')).toBe('warning')
    expect(screen.getByText('Done').closest('[data-sonner-toast]')?.getAttribute('data-type')).not.toBe('warning')
  })

  it('ignores a notice for another member', async () => {
    await deliver(notice({ member_id: 'mem_other' }))
    expect(screen.queryByText('Fix login finished')).toBeNull()
    expect(useStore.getState().assistantNotices).toBe(0)
  })

  it('counts a replayed notice on the badge without a toast', async () => {
    await deliver(notice({}, { time: new Date(now.getTime() - 10 * 60_000).toISOString() }))
    expect(screen.queryByText('Fix login finished')).toBeNull()
    expect(screen.getByRole('button', { name: 'Assistant, 1 update' })).toBeTruthy()
  })

  it('judges liveness on the server clock when the client clock is skewed', async () => {
    await act(async () => {
      await hydrate(useStore, fakeApi({ serverInfo: vi.fn(async () => ({ ...serverInfo, time: new Date(now.getTime() - 5 * 60_000).toISOString() })) }))
    })
    await deliver(notice({ title: 'Skewed clock' }, { time: new Date(now.getTime() - 5 * 60_000).toISOString() }))
    expect(await screen.findByText('Skewed clock')).toBeTruthy()
  })

  it('names requests and updates in the button label, and opening the panel clears the updates', async () => {
    useStore.setState({ assistantConfirmations: [{ id: 'c1', method: 'run.kill', summary: 's', params: {}, created_at: '', expires_at: '' }] })
    await deliver(notice())
    await deliver(notice({}, { seq: 8 }))
    const button = screen.getByRole('button', { name: 'Assistant, 1 request waiting, 2 updates' })
    await userEvent.click(button)
    expect(useStore.getState().assistantNotices).toBe(0)
    expect(screen.getByRole('button', { name: 'Assistant, 1 request waiting' })).toBeTruthy()
  })

  it('does not badge while the panel is open', async () => {
    useStore.getState().setAssistantOpen(true)
    await deliver(notice())
    expect(useStore.getState().assistantNotices).toBe(0)
  })
})
