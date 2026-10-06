import { render } from '@testing-library/react'
import { FeedEntry } from '@/components/feed-entry'
import { useStore } from '@/store'
import { alice, workspace } from '@/test/fixtures'

beforeEach(() => {
  useStore.setState({ members: { [alice.id]: alice } })
})

test("renders a durable report as the agent's outcome without its identifiers", () => {
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
          next_action: 'review retained evidence',
          evidence_refs: Array.from({ length: 10 }, (_, index) => `evidence_${index}`),
        },
      }}
    />,
  )

  const text = document.body.textContent ?? ''
  expect(text).toContain('The agent reported success:')
  expect(text).toContain('Implemented and verified the change.')
  expect(text).toContain("Next: Review the worker's saved results")
  expect(text).not.toContain('report_01')
  expect(text).not.toContain('evidence_0')
})
