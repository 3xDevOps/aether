import { render, screen } from '@testing-library/react'
import { FeedEntry } from '@/components/feed-entry'
import { useStore } from '@/store'
import { alice, workspace } from '@/test/fixtures'

beforeEach(() => {
  useStore.setState({ members: { [alice.id]: alice } })
})

test('renders durable report outcome details and bounded evidence identifiers', () => {
  render(
    <FeedEntry
      event={{
        id: 'report-event',
        seq: 12,
        time: '2026-08-14T10:00:00Z',
        workspace_id: workspace.id,
        run_id: 'run_1',
        actor_id: '',
        type: 'workspace.timeline',
        payload: {
          kind: 'report',
          report_id: 'report_01',
          outcome: 'success',
          summary: 'Implemented and verified the change.',
          next_action: 'Review retained evidence',
          evidence_refs: Array.from({ length: 10 }, (_, index) => `evidence_${index}`),
        },
      }}
    />,
  )

  expect(screen.getByText('Report:')).toBeDefined()
  expect(screen.getByText('report_01')).toBeDefined()
  expect(screen.getByText('Outcome: success')).toBeDefined()
  expect(screen.getByText('Summary: Implemented and verified the change.')).toBeDefined()
  expect(screen.getByText('Next action: Review retained evidence')).toBeDefined()
  expect(screen.getByText('evidence_0')).toBeDefined()
  expect(screen.getByText('evidence_7')).toBeDefined()
  expect(screen.queryByText('evidence_8')).toBeNull()
  expect(screen.queryByText('evidence_9')).toBeNull()
})
