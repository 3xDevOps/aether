import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { cleanup, render, screen, within } from '@testing-library/react'
import { describeEvent } from '@/components/event-words'
import { FeedEntry } from '@/components/feed-entry'
import { eventLabel, typeLabel } from '@/lib/events'
import type { Event } from '@/lib/types'
import { useStore } from '@/store'
import { alice, bob, run } from '@/test/fixtures'

const repo = path.resolve(process.cwd(), '..')

function goConstants(dir: string, kind: string): string[] {
  const folder = path.join(repo, dir)
  return readdirSync(folder)
    .filter((file) => file.endsWith('.go') && !file.endsWith('_test.go'))
    .flatMap((file) => [...readFileSync(path.join(folder, file), 'utf8').matchAll(new RegExp(`\\b${kind}\\s*=\\s*"([^"]+)"`, 'g'))].map((m) => m[1]))
}

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
  const serverTypes = goConstants('internal/events/', 'Type[A-Za-z]*\\s+Type')

  it('reads every event type the server declares', () => {
    expect(serverTypes.length).toBeGreaterThan(20)
    expect(serverTypes.filter((type) => !Object.hasOwn(eventLabel, type))).toEqual([])
  })

  it.each(serverTypes)('describes %s in words, without ids or wire names', (type) => {
    const text = words(type, {})
    expect(text).not.toBe('')
    expect(text).not.toBe('Something changed')
    expect(text).not.toContain(type)
    expect(text).not.toMatch(/\b(run|mem|wsp|room)[-_][a-z0-9]+/i)
  })

  it.each(goConstants('internal/events/', 'Timeline[A-Za-z]+\\s+TimelineKind'))('words the %s timeline entry', (kind) => {
    const text = words('workspace.timeline', { kind, message: kind === 'handoff' ? alice.id : 'work on the parser' })
    expect(text).not.toBe('Timeline entry')
    expect(text).not.toContain(alice.id)
  })

  it.each(goConstants('internal/protocol/', 'RoomMessage[A-Za-z]+\\s+RoomMessageState'))('words a teammate message that %s', (state) => {
    const text = words('workspace.room_message', { kind: 'steer_request', state, actor_id: alice.id, message_id: 'room-6yc510mxzf' })
    expect(text).toMatch(/^A message from Alice /)
    expect(text).not.toMatch(/_|room-/)
  })

  it.each([
    ['run.status', { to: 'needs-attention', reason: 'agent idle' }, alice.id, 'Needs you: agent idle'],
    ['run.status', { to: 'failed', reason: 'agent exited 1' }, alice.id, 'Failed: Agent exited with code 1'],
    ['run.status', { to: 'completed', reason: 'worker finished; retained container' }, alice.id, 'Finished: Worker finished'],
    ['run.status', { to: 'abandoned', reason: 'killed' }, alice.id, 'Stopped'],
    ['workspace.timeline', { kind: 'steer', message: 'use the JSON parser' }, bob.id, 'You messaged the agent: “use the JSON parser”'],
    ['workspace.timeline', { kind: 'handoff', message: bob.id }, alice.id, 'Alice handed the run to you'],
    ['workspace.timeline', { kind: 'report', outcome: 'success', summary: 'Done.', next_action: 'review retained evidence' }, '', "The agent reported success:Done.Next: Review the worker's saved results"],
    ['workspace.approval', { action: 'Bash: rm -rf build', decision: 'approved' }, alice.id, 'Alice approved “Bash: rm -rf build”'],
    ['workspace.room_message', { kind: 'steer_request', state: 'sent', actor_id: bob.id }, bob.id, 'A message from you reached the agent'],
    ['run.controller', { member_id: alice.id }, '', 'Alice took control'],
    ['run.controller', { member_id: '' }, '', 'Control released'],
    ['workspace.budget', { state: 'warn', spend_usd: 12.4, limit_usd: 50, unmetered_runs: 1 }, '', 'Budget nearing the cap, at least $12.40 spent of $50.00'],
    ['coord.message', { from_run_id: 'run_planner', to_run_id: 'run_backend', kind: 'question' }, '', 'PlannerBackend· Question'],
  ])('reads %s %j', (type, payload, actor, expected) => {
    expect(words(type, payload, actor)).toBe(expected)
  })

  it('keeps an unknown reason exactly as the server wrote it', () => {
    expect(words('run.status', { to: 'failed', reason: 'disk quota exceeded' })).toBe('Failed: disk quota exceeded')
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
