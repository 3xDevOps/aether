import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, type Api } from '@/lib/api'
import type { Mission, MissionAttempt, MissionQuestion, MissionTask, RunMessage } from '@/lib/types'
import { MissionRoute } from '@/routes/missions'
import { useStore, type RootState } from '@/store'
import { toRecord } from '@/store/runs'
import {
  agentInfo,
  alice,
  bob,
  fakeApi,
  mission,
  missionQuestion,
  missionTask,
  missionTaskRevision,
  run,
  serverInfo,
  workspace,
} from '@/test/fixtures'

const now = Date.parse('2026-08-14T10:20:00Z')

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(now)
})

afterEach(() => {
  vi.useRealTimers()
})

function seed(extra: Partial<RootState> = {}) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    runs: {},
    missions: {},
    missionDetails: {},
    missionError: null,
    missionLoading: false,
    messageLists: {},
    runMessages: {},
    messageErrors: {},
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    route: { name: 'missions', params: {} },
    ...extra,
  })
}

const integrator = (over: Parameters<typeof run>[0] = {}) =>
  run({ id: 'run_integrator', mission_id: 'mission_1', mission_role: 'integrator', task: 'coordinate', ...over })
const worker = (id: string, over: Parameters<typeof run>[0] = {}) =>
  run({ id, mission_id: 'mission_1', mission_role: 'worker', integrator_run_id: 'run_integrator', mode: 'headless', ...over })

function attempt(over: Partial<MissionAttempt> = {}): MissionAttempt {
  return {
    id: 'attempt_1', mission_id: 'mission_1', task_id: 'task_1', task_revision: 1, number: 1, dispatch_key: 'k',
    harness: 'claude', mode: 'headless', state: 'running', run_id: 'run_worker', authority_generation: 1,
    integrator_generation: 1, created_at: '2026-08-14T10:03:00Z', reserved_at: '2026-08-14T10:03:00Z', ...over,
  }
}

function message(over: Partial<RunMessage>): RunMessage {
  return {
    id: 'msg', workspace_id: workspace.id, mission_id: 'mission_1', from_run_id: 'run_worker', to_run_id: 'run_integrator',
    kind: 'message', body: 'hello', created_at: '2026-08-14T10:10:00Z', ...over,
  }
}

function showing(over: Partial<Mission> = {}, parts: {
  questions?: MissionQuestion[]
  tasks?: MissionTask[]
  attempts?: MissionAttempt[]
  messages?: RunMessage[]
} = {}): Api {
  return fakeApi({
    agentList: vi.fn(async () => [agentInfo({ display_name: 'Claude Code' })]),
    missionShow: vi.fn(async () => ({
      mission: mission(over),
      tasks: parts.tasks ?? [],
      attempts: parts.attempts ?? [],
      submissions: [],
      diagnostics: [],
      questions: parts.questions ?? [],
    })),
    coordMessagesList: vi.fn(async () => ({ messages: [...(parts.messages ?? [])].reverse() })),
  })
}

async function mount(client: Api, missionID: string | null = 'mission_1') {
  const view = render(<MissionRoute params={missionID ? { missionId: missionID } : {}} client={client} />)
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
  return view
}

describe('swarm list', () => {
  it('shows each swarm with its phase, integrator, counts and unread mail, needs-you first', async () => {
    seed({
      runs: Object.fromEntries([
        integrator({ unacked_messages: 3, oldest_unacked_at: '2026-08-14T10:08:00Z' }),
        worker('run_w1'),
        worker('run_w2', { status: 'completed' }),
        run({ id: 'run_asker', mission_id: 'mission_2', mission_role: 'integrator' }),
      ].map((r) => [r.id, toRecord(r)])),
    })
    const client = fakeApi({
      agentList: vi.fn(async () => [agentInfo({ display_name: 'Claude Code' })]),
      missionList: vi.fn(async () => ({
        missions: [
          mission({ updated_at: '2026-08-14T10:15:00Z' }),
          mission({ id: 'mission_2', objective: 'audit the logs', phase: 'planning', open_questions: 1, current_integrator_run_id: 'run_asker', integrator: { account_member_id: alice.id, harness: 'claude', mode: 'acp' } }),
          mission({ id: 'mission_3', objective: 'old spike', phase: 'cancelled' }),
        ],
      })),
      missionShow: vi.fn(),
    })
    await mount(client, null)

    const cards = within(await screen.findByRole('list', { name: 'Swarms' })).getAllByRole('article')
    expect(cards.map((card) => within(card).getByRole('button').textContent)).toEqual(['audit the logs', 'coordinate checkout work'])
    expect(cards[0].textContent).toContain('The integrator has a question')
    expect(cards[0].textContent).toContain('Claude Code · Enhanced')
    expect(cards[1].textContent).toContain('3 agent messages unread for 12 min')
    expect(cards[1].textContent).toContain('Claude Code · Standard')
    expect(cards[1].textContent).toContain('1 working · 1 done')
    expect(cards[1].textContent).toContain('3 unread')
    expect(screen.queryByText('old spike')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Finished (1)' }))
    expect(screen.getByText('old spike')).toBeDefined()
    expect(screen.queryByText(/mission_/)).toBeNull()
  })

  it('offers New swarm in the empty state and pages older swarms', async () => {
    seed()
    const missionList = vi.fn()
      .mockResolvedValueOnce({ missions: [], next_cursor: 'cursor-1' })
      .mockResolvedValueOnce({ missions: [mission({ objective: 'older swarm' })] })
    await mount(fakeApi({ missionList }), null)
    expect(await screen.findByRole('heading', { name: 'No swarms yet' })).toBeDefined()
    await userEvent.click(screen.getAllByRole('button', { name: 'New swarm' })[0])
    expect(useStore.getState().paletteDialog).toBe('launch')

    await userEvent.click(screen.getByRole('button', { name: 'Show more' }))
    expect(missionList.mock.calls[1][0]).toEqual({ workspace_id: workspace.id, limit: 50, before: 'cursor-1' })
    expect(await screen.findByText('older swarm')).toBeDefined()
  })
})

describe('swarm detail', () => {
  it('orders its sections and keeps internals behind Technical details', async () => {
    seed({ runs: { run_integrator: toRecord(integrator()) } })
    await mount(showing({}, { questions: [missionQuestion({ answer: 'guest', answered_at: '2026-08-14T10:02:00Z', answered_by_member_id: bob.id })], tasks: [missionTask()] }))
    const regions = screen.getAllByRole('region').map((region) => region.getAttribute('aria-label'))
    expect(regions.filter((name) => name !== 'Candidate review')).toEqual(['Swarm', 'Questions for you', 'Tasks', 'Agent messages', 'Integration'])
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('coordinate checkout work')
    expect(screen.getByText('Workers run. The integrator accepts their work, verifies and delivers the result, then reports success.')).toBeDefined()
    expect(screen.getByText('Answered by Bob')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Open integrator' })).toBeDefined()
    expect(screen.queryByText('mission_1')).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: 'Integration' }))
    await userEvent.click(screen.getByRole('button', { name: 'Technical details' }))
    expect(screen.getByText('mission_1')).toBeDefined()
    expect(screen.getByText('task_1 · revision 1')).toBeDefined()
  })

  it('answers an open question from the primary action', async () => {
    seed()
    const client = showing({ phase: 'planning', open_questions: 1 }, { questions: [missionQuestion()] })
    await mount(client)
    expect(screen.getByText(/Planning · 1 question for you/)).toBeDefined()
    await userEvent.click(within(screen.getByRole('toolbar')).getByRole('button', { name: 'Answer' }))
    expect(document.activeElement).toBe(screen.getByLabelText('Answer question 1'))
    fireEvent.change(screen.getByLabelText('Answer question 1'), { target: { value: 'the guest checkout flow' } })
    await act(async () => {
      fireEvent.click(within(screen.getByRole('region', { name: 'Questions for you' })).getByRole('button', { name: 'Answer' }))
    })
    expect(vi.mocked(client.missionQuestionAnswer).mock.calls[0][0]).toEqual({
      question_id: 'question_1',
      answer: 'the guest checkout flow',
      idempotency_key: 'question-answer-question_1',
    })
  })

  it('keeps an unsent draft when the answer arrives from elsewhere', async () => {
    seed()
    await mount(showing({ phase: 'planning', open_questions: 1 }, { questions: [missionQuestion()] }))
    fireEvent.change(screen.getByLabelText('Answer question 1'), { target: { value: 'half a thought' } })
    act(() => {
      useStore.getState().setMissionDetail({
        mission: mission({ phase: 'planning' }), tasks: [], attempts: [], submissions: [], diagnostics: [],
        questions: [missionQuestion({ answer: 'someone else answered', answered_by_member_id: bob.id, answered_at: '2026-08-14T10:03:00Z' })],
      })
    })
    expect((screen.getByLabelText('Answer question 1') as HTMLTextAreaElement).value).toBe('half a thought')
    expect(screen.getByText(/Answered by Bob while you were typing: someone else answered/)).toBeDefined()
  })

  it('shows no composer to a member who may not answer', async () => {
    seed({ info: { ...serverInfo, member: bob } })
    await mount(showing({ phase: 'planning', open_questions: 1 }, { questions: [missionQuestion()] }))
    expect(screen.queryByLabelText('Answer question 1')).toBeNull()
    expect(screen.getByText('Only Alice or an admin can answer.')).toBeDefined()
  })

  it('lists tasks with status, mode and a worker link, and releases a human hold', async () => {
    seed({ runs: { run_integrator: toRecord(integrator()) } })
    const client = showing({}, {
      tasks: [missionTask({ status: 'working' }), missionTask({ id: 'task_2', revision: missionTaskRevision({ task_id: 'task_2', title: 'add empty cart tests' }) })],
      attempts: [attempt({ takeover_active: true, takeover_member_id: alice.id, takeover_generation: 4 })],
    })
    await mount(client)
    const rows = within(screen.getByRole('region', { name: 'Tasks' })).getAllByRole('listitem')
    expect(rows[0].textContent).toContain('rewrite the guest checkout flow')
    expect(rows[0].textContent).toContain('Working')
    expect(rows[0].textContent).toContain('Background')
    expect(rows[0].textContent).toContain('Alice holds control of this worker')
    expect(rows[1].textContent).toContain('No worker yet')

    await userEvent.click(within(rows[0]).getByRole('button', { name: /Claude Code/ }))
    expect(useStore.getState().route).toMatchObject({ name: 'run', params: { runId: 'run_worker' } })
    await userEvent.click(within(rows[0]).getByRole('button', { name: 'Release control' }))
    expect(client.missionWorkerRelease).toHaveBeenCalledWith({ run_id: 'run_worker', expected_takeover_generation: 4 })
  })

  it('groups agent messages by thread and folds a run of plain messages', async () => {
    seed({ runs: { run_integrator: toRecord(integrator()), run_worker: toRecord(worker('run_worker')) } })
    await mount(showing({}, {
      tasks: [missionTask()],
      attempts: [attempt()],
      messages: [
        message({ id: 'm1', body: 'started', created_at: '2026-08-14T10:01:00Z' }),
        message({ id: 'm2', body: 'found two call sites', created_at: '2026-08-14T10:02:00Z' }),
        message({ id: 'q1', kind: 'question', correlation_id: 'q1', body: 'keep the template?', created_at: '2026-08-14T10:03:00Z' }),
        message({ id: 'r1', kind: 'reply', correlation_id: 'q1', from_run_id: 'run_integrator', to_run_id: 'run_worker', body: 'switch it', created_at: '2026-08-14T10:04:00Z', acked_at: '2026-08-14T10:05:00Z' }),
      ],
    }))
    const section = within(screen.getByRole('region', { name: 'Agent messages' }))
    expect(section.getByText('keep the template?')).toBeDefined()
    expect(section.getByText('switch it')).toBeDefined()
    expect(section.getByText('Acknowledged')).toBeDefined()
    expect(section.queryByText('started')).toBeNull()
    await userEvent.click(section.getByRole('button', { name: '2 messages' }))
    expect(section.getByText('started')).toBeDefined()
    expect(section.getAllByRole('button', { name: 'rewrite the guest checkout flow' }).length).toBeGreaterThan(0)
    await userEvent.click(section.getAllByRole('button', { name: 'Integrator' })[0])
    expect(useStore.getState().route).toMatchObject({ name: 'run', params: { runId: 'run_integrator' } })
  })

  it('says how long the integrator has left mail unread', async () => {
    seed({ runs: { run_integrator: toRecord(integrator({ unacked_messages: 3, oldest_unacked_at: '2026-08-14T10:08:00Z' })) } })
    await mount(showing())
    expect(screen.getAllByText(/3 agent messages unread for 12 min · Claude Code · Standard/).length).toBeGreaterThan(0)
  })

  it('cancels from More after confirmation, under one key across retries', async () => {
    seed({ runs: { run_integrator: toRecord(integrator()) } })
    const client = showing()
    vi.mocked(client.missionCancel).mockRejectedValueOnce(new Error('mission.cancel: try again'))
    await mount(client)
    await userEvent.click(screen.getByRole('button', { name: 'More swarm actions' }))
    await userEvent.click(screen.getByRole('menuitem', { name: 'Cancel swarm…' }))
    const confirm = screen.getByRole('alertdialog')
    await userEvent.click(within(confirm).getByRole('button', { name: 'Cancel swarm' }))
    expect(await within(confirm).findByText('mission.cancel: try again')).toBeDefined()
    await userEvent.click(within(confirm).getByRole('button', { name: 'Cancel swarm' }))
    const [first, second] = vi.mocked(client.missionCancel).mock.calls.map(([params]) => params.idempotency_key)
    expect(first).toBe(second)
  })

  it('hides Cancel swarm from a member who is neither accountable nor admin', async () => {
    seed({ info: { ...serverInfo, member: bob }, runs: { run_integrator: toRecord(integrator()) } })
    await mount(showing())
    await userEvent.click(screen.getByRole('button', { name: 'More swarm actions' }))
    expect(screen.getByRole('menuitem', { name: 'Replace integrator…' })).toBeDefined()
    expect(screen.queryByRole('menuitem', { name: 'Cancel swarm…' })).toBeNull()
  })

  it('replaces an integrator from its execution choices as Standard', async () => {
    seed({ runs: { run_integrator: toRecord(integrator({ status: 'failed' })) } })
    const client = showing({ integrator: { account_member_id: alice.id, harness: 'claude', mode: 'acp' } })
    await mount(client)
    const recovery = screen.getByText('The integrator is not running').parentElement!
    expect(recovery.textContent).toContain('The integrator run has exited; replace the integrator to continue.')
    await userEvent.click(within(recovery).getByRole('button', { name: 'Replace integrator…' }))
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Replace' }))
    expect(vi.mocked(client.missionReplaceIntegrator).mock.calls[0][0]).toMatchObject({
      mission_id: 'mission_1',
      expected_generation: 1,
      integrator: { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
    })
  })

  it('says the integrator has not started once the server has no run for it', async () => {
    seed()
    const client = showing({ integrator_launch_error: 'image pull failed', integrator_launch_error_at: '2026-08-14T10:10:00Z' })
    vi.mocked(client.runGet).mockRejectedValue(new ApiError(404, 'run.get: run run_integrator not found'))
    await mount(client)
    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.getByText(/The integrator run has not started/)).toBeDefined()
    expect(screen.getByText(/image pull failed/)).toBeDefined()
    expect(screen.getAllByText(/Integrator failed to launch/).length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: 'Open integrator' })).toBeNull()
  })
})

describe('swarm detail on a phone', () => {
  it('shows the objective and state line in the body, and leaves Answer to the question card', async () => {
    const original = window.matchMedia
    vi.spyOn(window, 'matchMedia').mockImplementation((query) =>
      Object.assign(new EventTarget(), { matches: query.includes('max-width'), media: query }) as unknown as MediaQueryList)
    onTestFinished(() => {
      window.matchMedia = original
    })
    seed()
    await mount(showing({ phase: 'planning', open_questions: 1 }, { questions: [missionQuestion()] }))
    const overview = within(screen.getByRole('region', { name: 'Swarm' }))
    expect(overview.getByText('coordinate checkout work')).toBeDefined()
    expect(overview.getByText(/Planning · 1 question for you/)).toBeDefined()
    expect(within(screen.getByRole('toolbar')).queryByRole('button', { name: 'Answer' })).toBeNull()
    expect(screen.getByLabelText('Answer question 1')).toBeDefined()
  })
})
