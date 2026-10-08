import { cleanup, render, screen, within } from '@testing-library/react'
import { describeEvent } from '@/components/event-words'
import { FeedEntry } from '@/components/feed-entry'
import { typeLabel } from '@/lib/events'
import type { Event } from '@/lib/types'
import { useStore } from '@/store'
import { alice, bob, run } from '@/test/fixtures'

function event(type: string, payload: unknown, actor = alice.id): Event {
  return { id: 'e1', seq: 1, time: '2026-08-14T11:00:00Z', workspace_id: 'wsp_1', run_id: 'run_1', actor_id: actor, type, payload }
}

function words(type: string, payload: unknown, actor?: string): string {
  const { container } = render(<>{describeEvent(event(type, payload, actor))}</>)
  const text = container.textContent ?? ''
  cleanup()
  return text
}

beforeEach(() => {
  useStore.setState({ members: { [alice.id]: alice, [bob.id]: bob }, info: { member: bob } as never })
  useStore.getState().upsertRun(run({ id: 'run_planner', title: 'Planner' }))
  useStore.getState().upsertRun(run({ id: 'run_backend', title: 'Backend' }))
})

describe('event words', () => {
  it('keeps an unknown reason exactly as the server wrote it', () => {
    expect(words('run.status', { to: 'failed', reason: 'disk quota exceeded' })).toContain('disk quota exceeded')
  })
})

describe('feed rows', () => {
  function renderRow(type: string, payload: unknown): HTMLElement {
    render(
      <ol>
        <FeedEntry event={event(type, payload)} />
      </ol>,
    )
    return screen.getByRole('listitem')
  }

  it('names the actor behind the colour', () => {
    const row = within(renderRow('run.status', { to: 'merged' }))
    expect(row.getByRole('img', { name: alice.display_name })).toBeDefined()
  })

  it('renders the retention deadline and cleanup diagnostic in a feed row', () => {
    const deadline = '2026-08-21T10:04:00Z'
    const cause = 'Execution cleanup failed; cleanup will retry'
    const row = renderRow('run.retention', {
      container_retained_until: deadline, cleanup_pending: true, cleanup_error: cause,
    })
    expect(row.querySelector(`time[datetime="${deadline}"]`)).not.toBeNull()
    expect(row.textContent).toContain(cause)
  })

  it('opens the sender of an agent message', () => {
    const row = renderRow('coord.message', { from_run_id: 'run_planner', to_run_id: 'run_backend', kind: 'question' })
    expect(row.textContent).toContain('Agent message sent')
    within(row).getByRole('button', { name: 'Planner' }).click()
    expect(useStore.getState().route).toMatchObject({ name: 'run', params: { runId: 'run_planner' } })
  })

  it('hides the wire name of a type this dashboard has never heard of', () => {
    expect(typeLabel('run.telepathy')).toBe('Other')
    expect(renderRow('run.telepathy', {}).textContent).not.toContain('run.telepathy')
  })
})
