import { render, screen, within } from '@testing-library/react'
import { FeedEntry } from '@/components/feed-entry'
import { typeLabel } from '@/lib/events'
import type { Event } from '@/lib/types'
import { useStore } from '@/store'
import { alice } from '@/test/fixtures'

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

  it('falls back to the wire string for a type it has never heard of', () => {
    expect(typeLabel('run.telepathy')).toBe('run.telepathy')
    expect(renderRow('run.telepathy', {}).textContent).toContain('run.telepathy')
  })
})
