import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { ApiError, type Api } from '@/lib/api'
import type {
  Mission,
  MissionAttempt,
  MissionQuestion,
  MissionSubmission,
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
    // Alice is both the accountable human and an admin; answering and
    // cancelling need a gateway that advertises the methods and such a member.
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
  tasks: MissionTask[] = [],
  attempts: MissionAttempt[] = [],
  submissions: MissionSubmission[] = [],
): Api {
  return fakeApi({
    missionShow: vi.fn(async () => ({
      mission: mission(over),
      tasks,
      attempts,
      submissions,
      diagnostics: [],
      questions,
    })),
  })
}

async function mount(client: Api): Promise<void> {
  render(<MissionRoute params={{ missionId: 'mission_1' }} client={client} />)
  await act(async () => {
    await Promise.resolve()
  })
}

describe('accepted mission evidence', () => {
  function submission(over: Partial<MissionSubmission> = {}): MissionSubmission {
    return {
      id: 'submission_1',
      mission_id: 'mission_1',
      task_id: 'task_1',
      task_revision: 1,
      attempt_id: 'attempt_1',
      ref: {
        workspace_id: workspace.id,
        run_id: 'run_worker',
        evidence_ref: 'evidence_1',
        retained_revision: 'retained-revision-1',
      },
      evidence: [{ kind: 'transcript', ref: 'evidence_1', available: true, truncated: true }],
      state: 'accepted',
      proposed_by_run_id: 'run_worker',
      integrator_generation: 1,
      created_at: '2026-08-14T10:04:00Z',
      ...over,
    }
  }

  function clientWith(submissions: MissionSubmission[]): Api {
    return showing({}, [], [missionTask({ status: 'done' })], [], submissions)
  }

  function taskCard() {
    return within(within(screen.getByRole('region', { name: 'Mission tasks' })).getByRole('article'))
  }

  it('distinguishes partial retained evidence from complete sources in the accepted receipt', async () => {
    seed()
    const detail = 'Retained <bounded> output\nEarlier bytes & lines omitted.'
    await mount(clientWith([submission({
      evidence: [
        { kind: 'transcript', ref: 'evidence_1', available: true, truncated: true, detail },
        { kind: 'git', ref: 'evidence_1', available: true },
      ],
    })]))

    const card = taskCard()
    expect(card.getByText(/Accepted submission/)).toBeDefined()
    const notes = within(card.getByRole('list', { name: 'Evidence exceptions at acceptance' }))
    expect(notes.getByText(/transcript: Partial retained evidence/)).toBeDefined()
    expect(notes.queryByText(/Unavailable at acceptance/)).toBeNull()
    expect(notes.queryByText(/git:/)).toBeNull()
    expect(card.getByText(/at acceptance, not a live availability check/)).toBeDefined()
    const source = notes.getByRole('listitem')
    expect(source.textContent).toContain(detail)
    expect(source.querySelector('bounded')).toBeNull()
  })

  it.each([false, true])('shows unavailable evidence rather than partial evidence when truncated is %s', async (truncated) => {
    seed()
    await mount(clientWith([submission({
      evidence: [{ kind: 'transcript', ref: 'evidence_1', available: false, truncated, detail: 'Artifact <expired> & removed.' }],
    })]))

    const card = taskCard()
    expect(card.getByText(/Accepted submission/)).toBeDefined()
    const notes = within(card.getByRole('list', { name: 'Evidence exceptions at acceptance' }))
    const source = notes.getByRole('listitem')
    expect(source.textContent).toContain('transcript: Unavailable at acceptance.')
    expect(notes.queryByText(/Partial retained evidence/)).toBeNull()
    expect(source.textContent).toContain('Artifact <expired> & removed.')
    expect(source.querySelector('expired')).toBeNull()
  })

  it.each([undefined, false])('keeps complete accepted evidence quiet when truncated is %s', async (truncated) => {
    seed()
    await mount(clientWith([submission({
      evidence: [{ kind: 'transcript', ref: 'evidence_1', available: true, truncated }],
    })]))

    const card = taskCard()
    expect(card.getByText(/Accepted submission/)).toBeDefined()
    expect(card.queryByRole('list', { name: 'Evidence exceptions at acceptance' })).toBeNull()
    expect(card.queryByText(/Partial retained evidence|Unavailable at acceptance/)).toBeNull()
  })

  it.each([
    ['stale revision', { task_revision: 0 }],
    ['proposed submission', { state: 'proposed' }],
    ['another task', { task_id: 'task_2' }],
  ] satisfies [string, Partial<MissionSubmission>][])('does not present %s facts as accepted for this task', async (_, over) => {
    seed()
    await mount(clientWith([submission(over)]))

    const card = taskCard()
    expect(card.queryByText(/Accepted submission/)).toBeNull()
    expect(card.queryByRole('list', { name: 'Evidence exceptions at acceptance' })).toBeNull()
  })

  it('uses only current accepted facts when stale and proposed submissions are also present', async () => {
    seed()
    await mount(clientWith([
      submission({ id: 'stale', task_revision: 0 }),
      submission({ id: 'proposed', state: 'proposed' }),
      submission({ evidence: [{ kind: 'transcript', ref: 'evidence_1', available: true }] }),
    ]))

    const card = taskCard()
    expect(card.getByText(/Accepted submission/)).toBeDefined()
    expect(card.queryByRole('list', { name: 'Evidence exceptions at acceptance' })).toBeNull()
  })

  it('replaces proposed and older accepted observations with the current server receipt on refresh', async () => {
    seed()
    const old = submission({ id: 'old', task_revision: 0, evidence: [{ kind: 'git', ref: 'evidence_old', available: false }] })
    const client = clientWith([old, submission({ state: 'proposed' })])
    await mount(client)
    expect(taskCard().queryByText(/Accepted submission/)).toBeNull()

    const current = submission()
    vi.mocked(client.missionShow).mockResolvedValue({
      mission: mission(),
      tasks: [missionTask({ status: 'done' })],
      attempts: [],
      submissions: [old, current],
      diagnostics: [],
      questions: [],
    })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    })

    const card = taskCard()
    expect(card.getByText(/Accepted submission/)).toBeDefined()
    const notes = within(card.getByRole('list', { name: 'Evidence exceptions at acceptance' }))
    expect(notes.getByText(/transcript: Partial retained evidence/)).toBeDefined()
    expect(notes.queryByText(/git:|Unavailable at acceptance/)).toBeNull()

    vi.mocked(client.missionShow).mockResolvedValue({
      mission: mission(),
      tasks: [missionTask({ status: 'done', current_revision: 2, revision: missionTaskRevision({ revision: 2 }) })],
      attempts: [],
      submissions: [
        current,
        submission({ id: 'next', task_revision: 2, evidence: [{ kind: 'transcript', ref: 'evidence_2', available: true }] }),
      ],
      diagnostics: [],
      questions: [],
    })
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    })

    expect(taskCard().getByText(/Accepted submission · revision 2/)).toBeDefined()
    expect(taskCard().queryByRole('list', { name: 'Evidence exceptions at acceptance' })).toBeNull()
  })
})

describe('mission questions', () => {
  it('offers the answer form in planning to the accountable human', async () => {
    seed()
    const client = showing({ phase: 'planning', open_questions: 1 }, [missionQuestion()])
    await mount(client)
    expect(screen.getByLabelText('Answer question 1')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Answer' })).toBeDefined()
  })

  it('sends the answer with a key derived from the question', async () => {
    seed()
    const client = showing({ phase: 'planning', open_questions: 1 }, [missionQuestion()])
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
    const client = showing({ phase: 'planning', open_questions: 1 }, [missionQuestion()])
    await mount(client)
    fireEvent.change(screen.getByLabelText('Answer question 1'), {
      target: { value: 'half a thought' },
    })
    act(() => {
      useStore.getState().setMissionDetail({
        mission: mission({ phase: 'planning', open_questions: 0 }),
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
        })
    })
    expect((screen.getByLabelText('Answer question 1') as HTMLTextAreaElement).value).toBe('half a thought')
    expect(screen.getByText('Answered by Bob')).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Answer' })).toBeNull()
  })

  it('shows proposed tasks and offers no plan decision while planning', async () => {
    seed()
    const client = showing(
      { phase: 'planning' },
      [],
      [missionTask({ status: 'proposed', revision: missionTaskRevision({ status: 'proposed', title: 'rewrite the guest checkout flow' }) })],
    )
    await mount(client)
    const proposed = within(screen.getByRole('region', { name: 'Proposed tasks' }))
    expect(proposed.getByText('rewrite the guest checkout flow')).toBeDefined()
    for (const name of ['Approve', 'Request changes', 'Reject']) {
      expect(screen.queryByRole('button', { name })).toBeNull()
    }
  })

  it('names a pending revision on its task in active', async () => {
    seed()
    const client = showing(
      { phase: 'active' },
      [],
      [
        missionTask({
          pending_revision: missionTaskRevision({
            revision: 2,
            status: 'proposed',
            title: 'also replace the payment provider',
          }),
        }),
      ],
    )
    await mount(client)
    expect(screen.getByText('Revision pending')).toBeDefined()
    expect(screen.getByText('Revision 2: also replace the payment provider')).toBeDefined()
  })

  it('keeps the asked questions collapsed once the swarm is active', async () => {
    seed()
    await mount(showing({ phase: 'active' }, [
      missionQuestion({ answer: 'the guest checkout flow', answered_by_member_id: alice.id, answered_at: '2026-08-14T10:03:00Z' }),
    ]))
    expect(screen.getByRole('region', { name: 'Planning questions' })).toBeDefined()
    expect(screen.queryByLabelText('Answer question 1')).toBeNull()
  })

  it('hides the answer form when the gateway omits the method', async () => {
    seed({ capabilities: { gateway: 'remote', methods: ['mission.show'], ws: ['events', 'attach'] } })
    await mount(showing({ phase: 'planning', open_questions: 1 }, [missionQuestion()]))
    expect(screen.queryByLabelText('Answer question 1')).toBeNull()
    expect(screen.getByText('1. which checkout flow?')).toBeDefined()
  })
})

describe('finished missions', () => {
  it.each([
    ['completed', 'The integrator reported success. Leftover workers were stopped.'],
    ['cancelled', 'The swarm was cancelled. Its workers and integrator run are stopped.'],
  ] as const)('shows %s without recovery or cancel controls', async (phase, sentence) => {
    seed({ runs: { run_integrator: toRecord(run({ id: 'run_integrator', status: 'completed' })) } })
    await mount(showing({ phase }, [], [missionTask({ status: 'done' })]))
    const banner = within(screen.getByRole('region', { name: 'Mission phase' }))
    expect(banner.getByText(sentence)).toBeDefined()
    expect(banner.queryByText(/has exited/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Replace integrator' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Cancel swarm' })).toBeNull()
    expect(screen.getByRole('region', { name: 'Mission tasks' })).toBeDefined()
  })

  it('shows candidate progress without any candidate control', async () => {
    seed()
    await mount(showing({ phase: 'completed' }))
    const progress = within(screen.getByRole('region', { name: 'Mission candidate progress' }))
    expect(progress.getByText(/This view is read-only/)).toBeDefined()
    for (const name of ['Prepare candidate', 'Run verification', 'Request delivery', 'Approve delivery', 'Deliver candidate']) {
      expect(progress.queryByRole('button', { name })).toBeNull()
    }
    expect(progress.queryByLabelText('Target ref')).toBeNull()
  })
})

describe('mission cancel', () => {
  it.each(['planning', 'active'] as const)('offers Cancel swarm in %s', async (phase) => {
    seed()
    await mount(showing({ phase }))
    expect(screen.getByRole('button', { name: 'Cancel swarm' })).toBeDefined()
  })

  it('hides Cancel swarm from a member who is neither accountable nor admin', async () => {
    seed({ info: { ...serverInfo, member: bob } })
    await mount(showing({ phase: 'planning' }))
    expect(screen.queryByRole('button', { name: 'Cancel swarm' })).toBeNull()
  })

  it('offers Cancel swarm to an admin who is not the accountable human', async () => {
    seed()
    await mount(showing({ phase: 'planning', accountable_human_id: bob.id }))
    expect(screen.getByRole('button', { name: 'Cancel swarm' })).toBeDefined()
  })

  it('cancels the mission after confirmation and refreshes the detail', async () => {
    seed()
    const client = showing({ phase: 'planning' })
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
    const client = showing({ phase: 'active' })
    vi.mocked(client.missionCancel).mockRejectedValueOnce(
      new Error('mission.cancel: mission is in phase completed; the mission is completed'),
    )
    await mount(client)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel swarm' }))
    const dialog = within(screen.getByRole('alertdialog', { name: 'Cancel this swarm?' }))
    await act(async () => {
      fireEvent.click(dialog.getByRole('button', { name: 'Cancel swarm' }))
    })
    expect(dialog.getByRole('alert').textContent).toBe(
      'mission.cancel: mission is in phase completed; the mission is completed',
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
    return { ...showing({ phase: 'planning' }), runGet: vi.fn(runGet) }
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
    expect(banner.queryByText(/asks you clarifying questions only if/)).toBeNull()
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
      ...showing({ phase: 'planning', ...launchFailure }),
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
    await mount(showing({ phase: 'active', ...launchFailure }))
    const banner = within(screen.getByRole('region', { name: 'Mission phase' }))
    expect(banner.getByText(/has exited; replace the integrator to continue/)).toBeDefined()
    expect(banner.getByText(/^Last launch failure/).textContent).toContain('is not installed for account alice')
  })

  it('names no launch failure on a cancelled swarm card', async () => {
    seed({ route: { name: 'missions', params: {} } })
    render(
      <MissionRoute
        params={{}}
        client={fakeApi({ missionList: vi.fn(async () => ({ missions: [mission({ ...launchFailure, phase: 'cancelled' })] })) })}
      />,
    )
    expect(await screen.findByText('Cancelled')).toBeDefined()
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

  it.each(['active', 'planning'] as const)('says the integrator run was deleted once it had launched, in %s', async (phase) => {
    seed()
    const client = {
      ...showing({ phase, integrator_run_launched: true }),
      runGet: vi.fn(async () => {
        throw new ApiError(404, 'run.get: run not found')
      }),
    }
    await mount(client)
    await act(async () => {
      await Promise.resolve()
    })
    const banner = within(screen.getByRole('region', { name: 'Mission phase' }))
    expect(banner.getByText('The integrator run was deleted; replace the integrator or cancel the swarm.')).toBeDefined()
    expect(banner.queryByText(/has not started/)).toBeNull()
    expect(banner.getByRole('button', { name: 'Replace integrator' })).toBeDefined()
  })

  it('keeps the planning copy and no run button while the server is asked', async () => {
    seed()
    await mount(withRunGet(() => new Promise(() => {})))
    expect(screen.getByText(/asks you clarifying questions only if/)).toBeDefined()
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
    expect(screen.getByText(/asks you clarifying questions only if/)).toBeDefined()
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
    expect(screen.getByText(/asks you clarifying questions only if/)).toBeDefined()
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
    await mount(showing({ phase: 'planning', objective }))
    const heading = screen.getByRole('heading', { level: 1 })
    expect(heading.textContent).toBe(`${firstLine.slice(0, 80).trimEnd()}…`)
    expect(heading.getAttribute('title')).toBe(objective)
    const section = within(screen.getByRole('region', { name: 'Mission objective' }))
    const full = section.getByText((_, node) => node?.tagName === 'P' && node.textContent === objective)
    expect(full.className).toContain('line-clamp-3')
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
