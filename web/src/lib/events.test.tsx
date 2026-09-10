import { cleanup, render, screen, within } from '@testing-library/react'
import { FeedEntry } from '@/components/feed-entry'
import { eventLabel, typeLabel, type EventType } from '@/lib/events'
import type { Event } from '@/lib/types'
import { useStore } from '@/store'
import { alice } from '@/test/fixtures'

/** One representative payload per named type. */
const samples: Record<EventType, unknown> = {
  'run.status': { to: 'needs-attention', reason: 'plan review' },
  'run.title': { title: 'rewrite the checkout flow' },
  'run.deleted': {},
  'run.protected': { protected: true },
  'run.agent': { kind: 'tool', tool: 'Bash', detail: 'go test ./...' },
  'run.diff': {
    files: [
      { path: 'src/a.ts', additions: 3, deletions: 1 },
      { path: 'src/b.ts', additions: 0, deletions: 8 },
    ],
  },
  'run.cost': { input_tokens: 120, output_tokens: 40 },
  'run.overlap': { with: [{ run_id: 'run_2', files: ['src/a.ts'] }] },
  'workspace.timeline': { kind: 'inject', message: 'try again' },
  'workspace.approval': { action: 'Bash', decision: 'approved' },
  'workspace.presence': { state: 'watching' },
  'workspace.budget': { state: 'warn', spend_usd: 8.5, limit_usd: 10 },
  'git.branch': { branch: 'run/1', commit: 'abc1234' },
  'sync.conflict': { files: ['src/a.ts'], run_id: 'run_1' },
  'server.update': { phase: 'applying', version: 'v0.2.0' },
}

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
  it('gives every type it names both a name and a description', () => {
    for (const type of Object.keys(eventLabel) as EventType[]) {
      const text = renderRow(type, samples[type]).textContent ?? ''
      expect(text, type).toContain(eventLabel[type])
      const described = text.split(eventLabel[type])[1] ?? ''
      expect(described.trim(), type).not.toBe('')
      cleanup()
    }
  })

  it('keeps the wire string as the tooltip, for the reader who needs it', () => {
    renderRow('run.status', { to: 'merged' })

    // By title, so deleting the tooltip fails here rather than passing on a
    // null the assertion never looked at.
    expect(screen.getByTitle('run.status').textContent).toBe('Run status')
  })

  // The dot's colour is the only other thing that says who acted.
  it('names the actor behind the colour', () => {
    useStore.setState({ members: { [alice.id]: alice } })
    const row = within(renderRow('run.status', { to: 'merged' }))

    expect(row.getByRole('img', { name: alice.display_name })).toBeDefined()
  })

  it('falls back to the wire string for a type it has never heard of', () => {
    expect(typeLabel('run.telepathy')).toBe('run.telepathy')
    expect(renderRow('run.telepathy', {}).textContent).toContain('run.telepathy')
  })
})
