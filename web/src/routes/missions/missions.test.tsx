import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError, type Api } from '@/lib/api'
import type {
  Mission,
  MissionAttempt,
  MissionPlanReview,
  MissionQuestion,
  MissionTask,
} from '@/lib/types'
import { MissionRoute } from '@/routes/missions'
import { toRecord } from '@/store/runs'
import { openSelect } from '@/test/select'
import { useStore, type RootState } from '@/store'
import {
  alice,
  bob,
  fakeApi,
  mission,
  missionAttempt,
  missionPlanItem,
  missionPlanReview,
  missionQuestion,
  missionTask,
  missionTaskRevision,
  run,
  serverInfo,
  workspace,
} from '@/test/fixtures'

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
    // Alice is both the accountable human and an admin; the gate needs a
    // gateway that advertises the methods and a member who may decide.
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
    route: { name: 'missions', params: { missionId: 'mission_1' } },
    ...extra,
  })
}

function showing(
  over: Partial<Mission>,
  questions: MissionQuestion[] = [],
  planReviews: MissionPlanReview[] = [],
  tasks: MissionTask[] = [],
  attempts: MissionAttempt[] = [],
): Api {
  return fakeApi({
    missionShow: vi.fn(async () => ({
      mission: mission(over),
      tasks,
      attempts,
      submissions: [],
      diagnostics: [],
      questions,
      plan_reviews: planReviews,
    })),
  })
}

async function mount(client: Api): Promise<void> {
  render(<MissionRoute params={{ missionId: 'mission_1' }} client={client} />)
  await act(async () => {
    await Promise.resolve()
  })
}

describe('mission plan gate', () => {
  it('offers the answer form in planning to the accountable human', async () => {
    seed()
    const client = showing({ phase: 'planning', plan_version: 0, open_questions: 1 }, [missionQuestion()])
    await mount(client)
    expect(screen.getByLabelText('Answer question 1')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Answer' })).toBeDefined()
  })

  it('sends the answer with a key derived from the question', async () => {
    seed()
    const client = showing({ phase: 'planning', plan_version: 0, open_questions: 1 }, [missionQuestion()])
    await mount(client)
    fireEvent.change(screen.getByLabelText('Answer question 1'), {
      target: { value: 'the guest checkout flow' },
    })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Answer' }))
    })
    expect(vi.mocked(client.missionQuestionAnswer).mock.calls[0][0]).toEqual({
      question_id: 'question_1',
      answer: 'the guest checkout flow',
      idempotency_key: 'question-answer-question_1',
    })
  })

  it('keeps an unsent draft visible when the answer arrives from elsewhere', async () => {
    seed()
    const client = showing({ phase: 'planning', plan_version: 0, open_questions: 1 }, [missionQuestion()])
    await mount(client)
    fireEvent.change(screen.getByLabelText('Answer question 1'), {
      target: { value: 'half a thought' },
    })
    act(() => {
      useStore.getState().setMissionDetail({
        mission: mission({ phase: 'planning', plan_version: 0, open_questions: 0 }),
        tasks: [],
        attempts: [],
        submissions: [],
        diagnostics: [],
        questions: [
          missionQuestion({
            answer: 'someone else answered',
            answered_by_member_id: bob.id,
            answered_at: '2026-08-14T10:03:00Z',
          }),
        ],
        plan_reviews: [],
      })
    })
    expect((screen.getByLabelText('Answer question 1') as HTMLTextAreaElement).value).toBe('half a thought')
    expect(screen.getByText('Answered by Bob')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Answer' })).toBeNull()
  })

  it('offers the three decisions in plan_review', async () => {
    seed()
    const client = showing(
      { phase: 'plan_review', plan_version: 2 },
      [missionQuestion({ answer: 'the guest checkout flow', answered_by_member_id: alice.id, answered_at: '2026-08-14T10:03:00Z' })],
      [missionPlanReview({ plan_version: 2 })],
    )
    await mount(client)
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Request changes' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Reject' })).toBeDefined()
  })

  it('sends the observed plan version and a per-decision key', async () => {
    seed()
    const client = showing({ phase: 'plan_review', plan_version: 2 }, [], [missionPlanReview({ plan_version: 2 })])
    await mount(client)
    fireEvent.change(screen.getByLabelText('Feedback (required to request changes)'), {
      target: { value: 'split the migration out' },
    })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Request changes' }))
    })
    expect(vi.mocked(client.missionPlanDecide).mock.calls[0][0]).toEqual({
      mission_id: 'mission_1',
      expected_plan_version: 2,
      decision: 'revise',
      feedback: 'split the migration out',
      idempotency_key: 'plan-decide-mission_1-2-revise',
    })
  })

  it('renders the decision read-only for a member who is neither accountable nor admin', async () => {
    seed({ info: { ...serverInfo, member: bob } })
    const client = showing({ phase: 'plan_review', plan_version: 2 }, [], [missionPlanReview({ plan_version: 2 })])
    await mount(client)
    expect(screen.getByText('split the checkout rewrite into two bounded tasks')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(screen.getByText('Only the accountable human or an admin may decide this plan.')).toBeDefined()
  })

  it('shows the questions without an answer form in clarified', async () => {
    seed()
    const client = showing({ phase: 'clarified', plan_version: 0 }, [
      missionQuestion({ answer: 'the guest checkout flow', answered_by_member_id: alice.id, answered_at: '2026-08-14T10:03:00Z' }),
    ])
    await mount(client)
    expect(screen.getByText('Clarification complete.')).toBeDefined()
    expect(screen.getByText('1. which checkout flow?')).toBeDefined()
    expect(screen.queryByLabelText('Answer question 1')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Answer' })).toBeNull()
  })

  it('renders the amendment round and keeps the approved work visible', async () => {
    seed()
    const client = showing(
      { phase: 'amendment_review', plan_version: 3 },
      [],
      [
        missionPlanReview({
          plan_version: 3,
          submitted_phase: 'active',
          summary: 'the payment provider needs a second task',
          items: [
            missionPlanItem({
              task_id: 'task_1',
              revision: 2,
              material: true,
              title: 'rewrite the guest checkout flow',
              supersedes_revision: 1,
              widening: ['web/payments/'],
            }),
            missionPlanItem({ task_id: 'task_2', revision: 1, new_task: true, title: 'swap the payment provider' }),
          ],
        }),
      ],
      [
        missionTask({
          status: 'working',
          pending_revision: missionTaskRevision({
            revision: 2,
            status: 'proposed',
            material: true,
            scope: { expected_paths: ['web/checkout/', 'web/payments/'] },
          }),
        }),
        missionTask({
          id: 'task_2',
          current_revision: 1,
          status: 'proposed',
          revision: missionTaskRevision({
            task_id: 'task_2',
            title: 'swap the payment provider',
            status: 'proposed',
            scope: { expected_paths: ['web/payments/'] },
          }),
        }),
      ],
      [missionAttempt()],
    )
    await mount(client)
    expect(screen.getByText('Changed task · rewrite the guest checkout flow')).toBeDefined()
    expect(screen.getByText('New work · swap the payment provider')).toBeDefined()
    expect(screen.getByText('Material')).toBeDefined()
    expect(screen.getByText('Expected web/payments/ · widens the approved scope')).toBeDefined()
    // Approved work keeps running while the human decides.
    expect(screen.getByRole('region', { name: 'Mission tasks' })).toBeDefined()
    expect(screen.getByText('Attempt 1 · running')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Request changes' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Reject' })).toBeNull()
  })

  it('sends the amendment decision with the observed plan version', async () => {
    seed()
    const client = showing(
      { phase: 'amendment_review', plan_version: 3 },
      [],
      [missionPlanReview({ plan_version: 3, submitted_phase: 'active', items: [missionPlanItem()] })],
      [missionTask({ pending_revision: missionTaskRevision({ revision: 2, status: 'proposed' }) })],
    )
    await mount(client)
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    })
    expect(vi.mocked(client.missionPlanDecide).mock.calls[0][0]).toEqual({
      mission_id: 'mission_1',
      expected_plan_version: 3,
      decision: 'approve',
      idempotency_key: 'plan-decide-mission_1-3-approve',
    })
  })

  it('shows the revision the amendment names for a new task revised again in active', async () => {
    seed()
    const client = showing(
      { phase: 'amendment_review', plan_version: 3 },
      [],
      [
        missionPlanReview({
          plan_version: 3,
          submitted_phase: 'active',
          items: [missionPlanItem({ task_id: 'task_2', revision: 2, new_task: true, title: 'swap the payment provider' })],
        }),
      ],
      [
        missionTask({
          id: 'task_2',
          current_revision: 1,
          status: 'proposed',
          revision: missionTaskRevision({ task_id: 'task_2', revision: 1, status: 'proposed', objective: 'the first draft' }),
          pending_revision: missionTaskRevision({ task_id: 'task_2', revision: 2, status: 'proposed', objective: 'the revised draft' }),
        }),
      ],
    )
    await mount(client)
    // The Tasks section still shows the current draft; the amendment card
    // must show the revision the round names.
    const amendment = within(screen.getByRole('region', { name: 'Amendment review' }))
    expect(amendment.getByText('the revised draft')).toBeDefined()
    expect(amendment.queryByText('the first draft')).toBeNull()
  })

  it('names a pending revision on the task it amends in active', async () => {
    seed()
    const client = showing(
      { phase: 'active', plan_version: 2 },
      [],
      [],
      [
        missionTask({
          pending_revision: missionTaskRevision({
            revision: 2,
            status: 'proposed',
            material: true,
            title: 'also replace the payment provider',
          }),
        }),
      ],
    )
    await mount(client)
    expect(screen.getByText('Material revision pending')).toBeDefined()
    expect(screen.getByText('Revision 2: also replace the payment provider')).toBeDefined()
  })

  it('hides both gate controls when the gateway omits the methods', async () => {
    seed({ capabilities: { gateway: 'remote', methods: ['mission.show'], ws: ['events', 'attach'] } })
    const planning = showing({ phase: 'planning', plan_version: 0, open_questions: 1 }, [missionQuestion()])
    await mount(planning)
    expect(screen.queryByLabelText('Answer question 1')).toBeNull()
    expect(screen.getByText('1. which checkout flow?')).toBeDefined()

    seed({ capabilities: { gateway: 'remote', methods: ['mission.show'], ws: ['events', 'attach'] } })
    const review = showing({ phase: 'plan_review', plan_version: 2 }, [], [missionPlanReview({ plan_version: 2 })])
    await mount(review)
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
  })
})

describe('mission cancel', () => {
  it.each(['planning', 'clarified', 'plan_review'] as const)('offers Cancel swarm in %s', async (phase) => {
    seed()
    await mount(showing({ phase, plan_version: phase === 'plan_review' ? 2 : 0 }))
    expect(screen.getByRole('button', { name: 'Cancel swarm' })).toBeDefined()
  })

  it('hides Cancel swarm once the plan is approved', async () => {
    seed()
    await mount(showing({ phase: 'active' }))
    expect(screen.queryByRole('button', { name: 'Cancel swarm' })).toBeNull()
  })

  it('hides Cancel swarm from a member who is neither accountable nor admin', async () => {
    seed({ info: { ...serverInfo, member: bob } })
    await mount(showing({ phase: 'planning', plan_version: 0 }))
    expect(screen.queryByRole('button', { name: 'Cancel swarm' })).toBeNull()
  })

  it('offers Cancel swarm to an admin who is not the accountable human', async () => {
    seed()
    await mount(showing({ phase: 'planning', plan_version: 0, accountable_human_id: bob.id }))
    expect(screen.getByRole('button', { name: 'Cancel swarm' })).toBeDefined()
  })

  it('cancels the mission after confirmation and refreshes the detail', async () => {
    seed()
    const client = showing({ phase: 'planning', plan_version: 0 })
    await mount(client)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel swarm' }))
    const dialog = within(screen.getByRole('alertdialog', { name: 'Cancel this swarm?' }))
    expect(client.missionCancel).not.toHaveBeenCalled()
    await act(async () => {
      fireEvent.click(dialog.getByRole('button', { name: 'Cancel swarm' }))
    })
    expect(vi.mocked(client.missionCancel).mock.calls[0][0]).toEqual({
      mission_id: 'mission_1',
      idempotency_key: expect.any(String),
    })
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(client.missionShow).toHaveBeenCalledTimes(2)
  })

  it('shows the refusal and retries under the same key', async () => {
    seed()
    const client = showing({ phase: 'plan_review', plan_version: 2 }, [], [missionPlanReview({ plan_version: 2 })])
    vi.mocked(client.missionCancel).mockRejectedValueOnce(
      new Error('mission.cancel: mission is in phase active; the plan has already been approved'),
    )
    await mount(client)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel swarm' }))
    const dialog = within(screen.getByRole('alertdialog', { name: 'Cancel this swarm?' }))
    await act(async () => {
      fireEvent.click(dialog.getByRole('button', { name: 'Cancel swarm' }))
    })
    expect(dialog.getByRole('alert').textContent).toBe(
      'mission.cancel: mission is in phase active; the plan has already been approved',
    )
    await act(async () => {
      fireEvent.click(dialog.getByRole('button', { name: 'Cancel swarm' }))
    })
    const [first, second] = vi.mocked(client.missionCancel).mock.calls.map(([params]) => params)
    expect(second.idempotency_key).toBe(first.idempotency_key)
  })
})

describe('mission integrator run', () => {
  function withRunGet(runGet: Api['runGet']): Api {
    return { ...showing({ phase: 'planning', plan_version: 0 }), runGet: vi.fn(runGet) }
  }

  it('says the integrator run has not started once the server has no run for it', async () => {
    seed()
    const client = withRunGet(async () => {
      throw new ApiError(404, 'run.get: run not found')
    })
    await mount(client)
    await act(async () => {
      await Promise.resolve()
    })
    expect(client.runGet).toHaveBeenCalledWith('run_integrator')
    const banner = within(screen.getByRole('region', { name: 'Mission phase' }))
    expect(banner.getByText(/^The integrator run has not started\./)).toBeDefined()
    expect(banner.queryByText(/may ask you clarifying questions/)).toBeNull()
    expect(banner.getByRole('button', { name: 'Replace integrator' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Open integrator run' })).toBeNull()
  })

  const launchFailure = {
    integrator_launch_error: 'run.launch: harness "claude" is not installed for account alice',
    integrator_launch_error_at: '2026-08-14T10:05:00Z',
  }

  it('says why the integrator has not started', async () => {
    seed()
    const client = {
      ...showing({ phase: 'planning', plan_version: 0, ...launchFailure }),
      runGet: vi.fn(async () => {
        throw new ApiError(404, 'run.get: run not found')
      }),
    }
    await mount(client)
    await act(async () => {
      await Promise.resolve()
    })
    const banner = within(screen.getByRole('region', { name: 'Mission phase' }))
    expect(banner.getByText(/^The integrator run has not started\./)).toBeDefined()
    const failure = banner.getByText(/^Last launch failure/)
    expect(failure.textContent).toContain(': run.launch: harness "claude" is not installed for account alice')
    expect(failure.querySelector('time')?.getAttribute('dateTime')).toBe('2026-08-14T10:05:00Z')
  })

  it('says why the replacement did not launch once the integrator has exited', async () => {
    seed({ runs: { run_integrator: toRecord(run({ id: 'run_integrator', status: 'failed' })) } })
    await mount(showing({ phase: 'plan_review', plan_version: 2, ...launchFailure }, [], [missionPlanReview({ plan_version: 2 })]))
    const banner = within(screen.getByRole('region', { name: 'Mission phase' }))
    expect(banner.getByText(/has exited; replace the integrator to continue/)).toBeDefined()
    expect(banner.getByText(/^Last launch failure/).textContent).toContain('is not installed for account alice')
  })

  it('names no launch failure on a rejected swarm card', async () => {
    seed({ route: { name: 'missions', params: {} } })
    render(
      <MissionRoute
        params={{}}
        client={fakeApi({ missionList: vi.fn(async () => ({ missions: [mission({ ...launchFailure, phase: 'rejected' })] })) })}
      />,
    )
    expect(await screen.findByText('Rejected')).toBeDefined()
    expect(screen.queryByText(/Integrator did not launch/)).toBeNull()
  })

  it('names the launch failure on the swarm card', async () => {
    seed({ route: { name: 'missions', params: {} } })
    render(
      <MissionRoute
        params={{}}
        client={fakeApi({ missionList: vi.fn(async () => ({ missions: [mission(launchFailure)] })) })}
      />,
    )
    expect(
      await screen.findByText('Integrator did not launch: run.launch: harness "claude" is not installed for account alice'),
    ).toBeDefined()
  })

  async function openReplacement(over: Partial<Mission>) {
    seed()
    const client = {
      ...showing({ phase: 'active', ...over }),
      runGet: vi.fn(async () => run({ id: 'run_integrator', status: 'failed' })),
    }
    await mount(client)
    fireEvent.click(within(screen.getByRole('region', { name: 'Mission authorization' })).getByRole('button', { name: 'Replace integrator' }))
    return { client, dialog: within(screen.getByRole('dialog', { name: 'Replace integrator' })) }
  }

  it('replaces a headless integrator as tui from its execution choices', async () => {
    // A swarm created before headless integrators were refused.
    const { client, dialog } = await openReplacement({
      integrator: { account_member_id: alice.id, harness: 'claude', mode: 'headless' },
      execution_choices: [{ account_member_id: alice.id, harness: 'claude', mode: 'headless' }],
    })
    expect(dialog.queryByText('Mode')).toBeNull()
    await act(async () => {
      fireEvent.click(dialog.getByRole('button', { name: 'Replace' }))
    })
    expect(vi.mocked(client.missionReplaceIntegrator).mock.calls[0][0].integrator).toEqual({
      account_member_id: alice.id,
      harness: 'claude',
      mode: 'tui',
    })
  })

  it('offers each execution choice account and harness once, and nothing else', async () => {
    const { dialog } = await openReplacement({
      execution_choices: [
        { account_member_id: alice.id, harness: 'claude', mode: 'headless' },
        { account_member_id: alice.id, harness: 'claude', mode: 'tui' },
        { account_member_id: alice.id, harness: 'codex', mode: 'headless' },
      ],
    })
    await openSelect(dialog.getAllByRole('combobox')[1])
    expect(screen.getAllByRole('option').map((option) => option.textContent)).toEqual(['claude', 'codex'])
  })

  it.each([
    ['active', 'The integrator run was deleted; replace the integrator.'],
    ['planning', 'The integrator run was deleted; replace the integrator or cancel the swarm.'],
  ] as const)('says the integrator run was deleted once it had launched, in %s', async (phase, sentence) => {
    seed()
    const client = {
      ...showing({ phase, plan_version: 1, integrator_run_launched: true }),
      runGet: vi.fn(async () => {
        throw new ApiError(404, 'run.get: run not found')
      }),
    }
    await mount(client)
    await act(async () => {
      await Promise.resolve()
    })
    const banner = within(screen.getByRole('region', { name: 'Mission phase' }))
    expect(banner.getByText(sentence)).toBeDefined()
    expect(banner.queryByText(/has not started/)).toBeNull()
    expect(banner.getByRole('button', { name: 'Replace integrator' })).toBeDefined()
  })

  it('keeps the planning copy and no run button while the server is asked', async () => {
    seed()
    await mount(withRunGet(() => new Promise(() => {})))
    expect(screen.getByText(/may ask you clarifying questions/)).toBeDefined()
    expect(screen.queryByText(/has not started/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Open integrator run' })).toBeNull()
  })

  it('stores a run the event stream has not delivered yet and shows its status', async () => {
    seed()
    const client = withRunGet(async () =>
      run({ id: 'run_integrator', status: 'needs-attention', reason: 'waiting for approval' }),
    )
    await mount(client)
    await act(async () => {
      await Promise.resolve()
    })
    expect(useStore.getState().runs.run_integrator?.status).toBe('needs-attention')
    const authorization = within(screen.getByRole('region', { name: 'Mission authorization' }))
    expect(authorization.getByText('Needs you')).toBeDefined()
    expect(authorization.getByText('waiting for approval')).toBeDefined()
    expect(authorization.getByRole('button', { name: 'Open integrator run' })).toBeDefined()
    expect(screen.queryByText(/has not started/)).toBeNull()
    expect(client.runGet).toHaveBeenCalledTimes(1)
  })

  it('hides the run button after a failed lookup and asks again on Refresh', async () => {
    seed()
    const client = withRunGet(async () => {
      throw new ApiError(503, 'run.get: server unavailable')
    })
    await mount(client)
    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.getByText('run.get: server unavailable')).toBeDefined()
    expect(screen.getByText(/may ask you clarifying questions/)).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Open integrator run' })).toBeNull()
    expect(client.runGet).toHaveBeenCalledTimes(1)

    vi.mocked(client.runGet).mockResolvedValue(run({ id: 'run_integrator' }))
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    })
    await act(async () => {
      await Promise.resolve()
    })
    expect(client.runGet).toHaveBeenCalledTimes(2)
    expect(useStore.getState().runs.run_integrator).toBeDefined()
    expect(screen.getByRole('button', { name: 'Open integrator run' })).toBeDefined()
    expect(screen.queryByText('run.get: server unavailable')).toBeNull()
  })

  it('does not ask the server before the runs have hydrated', async () => {
    seed({ hydrated: false })
    const client = withRunGet(async () => run({ id: 'run_integrator' }))
    await mount(client)
    expect(client.runGet).not.toHaveBeenCalled()
    expect(screen.getByText(/may ask you clarifying questions/)).toBeDefined()
  })
})

describe('mission objective', () => {
  const firstLine = 'Rework the checkout flow so that expired sessions are rejected before payment and the cart survives a login'
  const objective = `${firstLine}\nAlso audit the coupon path.\nAlso cover the guest checkout.`

  // jsdom lays nothing out, so a clamped paragraph never reports overflow on
  // its own; this stands in for three clamped lines hiding a fourth.
  function clip() {
    vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(80)
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(60)
  }

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('keeps the header to one short line and the full objective in the scroll area', async () => {
    seed()
    clip()
    await mount(showing({ phase: 'plan_review', plan_version: 2, objective }, [], [missionPlanReview({ plan_version: 2 })]))
    const heading = screen.getByRole('heading', { level: 1 })
    expect(heading.textContent).toBe(`${firstLine.slice(0, 80).trimEnd()}…`)
    expect(heading.getAttribute('title')).toBe(objective)
    const section = within(screen.getByRole('region', { name: 'Mission objective' }))
    const full = section.getByText((_, node) => node?.tagName === 'P' && node.textContent === objective)
    expect(full.className).toContain('line-clamp-3')
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDefined()
    expect(screen.getByRole('button', { name: 'Request changes' })).toBeDefined()
    fireEvent.click(section.getByRole('button', { name: 'Show more' }))
    expect(full.className).not.toContain('line-clamp-3')
    fireEvent.click(section.getByRole('button', { name: 'Show less' }))
    expect(full.className).toContain('line-clamp-3')
  })

  it('offers no toggle when the objective fits', async () => {
    seed()
    await mount(showing({ phase: 'active', objective: 'coordinate checkout work' }))
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('coordinate checkout work')
    expect(within(screen.getByRole('region', { name: 'Mission objective' })).queryByRole('button')).toBeNull()
  })

  it('clamps each objective on the missions list', async () => {
    seed({ route: { name: 'missions', params: {} } })
    render(<MissionRoute params={{}} client={fakeApi({ missionList: vi.fn(async () => ({ missions: [mission({ objective })] })) })} />)
    const card = await screen.findByText((_, node) => node?.tagName === 'P' && node.textContent === objective)
    expect(card.className).toContain('line-clamp-3')
  })
})
