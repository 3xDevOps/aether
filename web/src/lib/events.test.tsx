import { render, screen, within } from '@testing-library/react'
import { FeedEntry } from '@/components/feed-entry'
import { typeLabel } from '@/lib/events'
import type { Event } from '@/lib/types'
import { useStore } from '@/store'
import { alice, run } from '@/test/fixtures'

function renderRow(type: string, payload: unknown): HTMLElement {
  const event: Event = {
    id: 'e1',
    seq: 1,
    time: '2026-08-14T11:00:00Z',
    workspace_id: 'wsp_1',
    run_id: 'run_1',
    actor_id: 'mem_alice',
    type,
    payload,
  }
  render(
    <ol>
      <FeedEntry event={event} />
    </ol>,
  )
  return screen.getByRole('listitem')
}

describe('feed rows', () => {
  // The dot's colour is the only other thing that says who acted.
  it('names the actor behind the colour', () => {
    useStore.setState({ members: { [alice.id]: alice } })
    const row = within(renderRow('run.status', { to: 'merged' }))

    expect(row.getByRole('img', { name: alice.display_name })).toBeDefined()
  })

  it('names the owner opening a reported run', () => {
    const text = renderRow('run.outcome_seen', {}).textContent ?? ''
    expect(text).toContain('Outcome seen')
    expect(text).toContain('owner opened the finished run')
  })

  it('names both runs of an agent message and opens the sender', () => {
    useStore.getState().upsertRun(run({ id: 'run_planner', title: 'Planner' }))
    useStore.getState().upsertRun(run({ id: 'run_backend', title: 'Backend' }))
    const row = renderRow('coord.message', {
      message_id: 'msg-1', from_run_id: 'run_planner', to_run_id: 'run_backend', kind: 'question',
    })
    expect(row.textContent).toContain('Agent message')
    expect(row.textContent).toContain('Planner → Backend · question')

    within(row).getByRole('button', { name: 'Planner' }).click()
    expect(useStore.getState().route).toMatchObject({ name: 'run', params: { runId: 'run_planner' } })
  })

  it('falls back to the wire string for a type it has never heard of', () => {
    expect(typeLabel('run.telepathy')).toBe('run.telepathy')
    expect(renderRow('run.telepathy', {}).textContent).toContain('run.telepathy')
  })
})
