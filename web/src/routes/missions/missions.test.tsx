import { act, fireEvent, render, screen, within } from '@testing-library/react'
import type { Api } from '@/lib/api'
import type {
  Mission,
  MissionAttempt,
  MissionPlanReview,
  MissionQuestion,
  MissionTask,
} from '@/lib/types'
import { MissionRoute } from '@/routes/missions'
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
