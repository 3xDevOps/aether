import { render, screen } from '@testing-library/react'
import { useStore } from '@/store'
import { useNeedsYouCount } from '@/store/hooks'
import { toRecord } from '@/store/runs'
import { alice, bob, otherWorkspace, roomMessage, run, serverInfo, workspace } from '@/test/fixtures'

function Probe({ workspace }: { workspace?: string }) {
  return <output aria-label="needs you count">{useNeedsYouCount(workspace)}</output>
}

const count = () => screen.getByLabelText('needs you count').textContent

describe('useNeedsYouCount', () => {
  beforeEach(() => {
    useStore.setState({
      info: serverInfo,
      activeWorkspace: workspace.id,
      runs: {},
      roomMessages: {},
      members: { [alice.id]: alice, [bob.id]: bob },
      inbox: {},
      approvalsByRun: {},
    })
  })

  it('counts a run with both native input and a room question once', () => {
    const asked = run({
      id: 'asked',
      pending_inputs: [{ id: 'q1', session_id: 'session-1', kind: 'question' }],
    })
    useStore.setState({
      runs: { [asked.id]: toRecord(asked) },
      roomMessages: {
        [asked.id]: [roomMessage({ run_id: asked.id, actor_id: bob.id, kind: 'question', body: 'Need a decision' })],
      },
    })
    render(<Probe />)
    expect(count()).toBe('1')
  })

  it('counts unanswered questions from a fresh run snapshot before the room opens', () => {
    const unopened = run({ id: 'unopened', unanswered_questions: 1 })
    useStore.setState({ runs: { [unopened.id]: toRecord(unopened) } })
    render(<Probe />)
    expect(count()).toBe('1')
  })

  it('treats a zero snapshot count as authoritative over stale room history', () => {
    const answered = run({ id: 'answered', unanswered_questions: 0 })
    useStore.setState({
      runs: { [answered.id]: toRecord(answered) },
      roomMessages: {
        [answered.id]: [roomMessage({ run_id: answered.id, actor_id: bob.id, kind: 'question' })],
      },
    })
    render(<Probe />)
    expect(count()).toBe('0')
  })

  it('leaves out an archived, final run', () => {
    const archived = run({
      id: 'archived',
      status: 'merged',
      unanswered_questions: 1,
      archived_at: '2026-08-14T10:00:00Z',
      deletes_at: '2026-08-28T10:00:00Z',
    })
    useStore.setState({ runs: { [archived.id]: toRecord(archived) } })
    render(<Probe />)
    expect(count()).toBe('0')
  })

  it("counts the viewer's unreviewed outcomes, not another member's", () => {
    const mine = run({ id: 'mine', status: 'completed', outcome_unseen: true })
    const theirs = run({ id: 'theirs', member_id: bob.id, status: 'failed', outcome_unseen: true })
    useStore.setState({ runs: Object.fromEntries([mine, theirs].map((r) => [r.id, toRecord(r)])) })
    render(<Probe />)
    expect(count()).toBe('1')
  })

  it('counts every workspace, or one when named', () => {
    const here = run({ id: 'here', status: 'needs-attention' })
    const there = run({ id: 'there', workspace_id: otherWorkspace.id, status: 'needs-attention' })
    useStore.setState({ runs: Object.fromEntries([here, there].map((r) => [r.id, toRecord(r)])) })
    render(
      <>
        <Probe />
        <Probe workspace={otherWorkspace.id} />
      </>,
    )
    expect(screen.getAllByLabelText('needs you count').map((node) => node.textContent)).toEqual(['2', '1'])
  })
})
