import { render, screen } from '@testing-library/react'
import { useStore } from '@/store'
import { useAttentionCount } from '@/store/hooks'
import { toRecord } from '@/store/runs'
import { alice, roomMessage, run, workspace } from '@/test/fixtures'

function Probe() {
  return <output aria-label="attention count">{useAttentionCount()}</output>
}

describe('attention hooks', () => {
  beforeEach(() => {
    useStore.setState({
      activeWorkspace: workspace.id,
      groupBy: 'status',
      runs: {},
      roomMessages: {},
      members: {},
      inbox: {},
    })
  })

  it('counts a run with status and unanswered-question attention once', () => {
    const attention = run({ id: 'attention', status: 'needs-attention' })
    useStore.setState({
      runs: { [attention.id]: toRecord(attention) },
      members: { [alice.id]: alice },
      roomMessages: {
        [attention.id]: [roomMessage({ run_id: attention.id, kind: 'question', body: 'Need a decision' })],
      },
    })

    render(<Probe />)

    expect(screen.getByLabelText('attention count').textContent).toBe('1')
  })
  it('counts unanswered questions from a fresh run snapshot before room open', () => {
    const attention = run({ id: 'unopened', status: 'running', unanswered_questions: 1 })
    useStore.setState({
      runs: { [attention.id]: toRecord(attention) },
      members: { [alice.id]: alice },
    })

    render(<Probe />)

    expect(screen.getByLabelText('attention count').textContent).toBe('1')
  })

  it('treats a modern zero count as authoritative over stale room history', () => {
    const answered = run({ id: 'answered', status: 'running', unanswered_questions: 0 })
    useStore.setState({
      runs: { [answered.id]: toRecord(answered) },
      members: { [alice.id]: alice },
      roomMessages: {
        [answered.id]: [
          roomMessage({ run_id: answered.id, kind: 'question', body: 'Already answered' }),
        ],
      },
    })

    render(<Probe />)

    expect(screen.getByLabelText('attention count').textContent).toBe('0')
  })
})
