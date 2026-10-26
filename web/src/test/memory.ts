import { fireEvent, screen, within } from '@testing-library/react'
import type { MemoryItem, MemorySearchParams } from '@/lib/types'
import { useStore } from '@/store'
import { alice, bob, memoryItem, run, serverInfo, workspace } from '@/test/fixtures'

export function seedMemory(member = alice) {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice, [bob.id]: bob },
    info: { ...serverInfo, member },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    connection: 'live',
    hydrated: true,
    agentList: null,
  })
  useStore.getState().upsertRun(run({ id: 'run_1', title: 'Fix the reattach race' }))
}

export const agentNote = memoryItem({
  id: 'note-agent00001',
  kind: 'gotcha',
  source: 'agent',
  title: 'Reattach races the lease sweeper',
  body: 'Reattach races the lease sweeper\nTake the lease before reading the container state.',
  run_id: 'run_1',
  harness: 'claude',
  commit: '1a2b3c4d5e6f7a8b9c0d',
  pr_number: 312,
  pr_state: 'merged',
  pr_url: 'https://github.com/acme/app/pull/312',
  files: ['internal/scheduler/reattach.go', 'internal/scheduler/lease.go'],
  created_at: '2026-10-18T12:00:00Z',
})
export const workspacePin = memoryItem({ id: 'note-pinned0001', pinned_by: bob.id, pinned_by_name: 'Bob', pinned_at: '2026-10-18T10:00:00Z' })
export const myPin = memoryItem({
  id: 'note-mine000001', kind: 'preference', scope: 'member', title: 'No emoji in commit messages.', body: 'No emoji in commit messages.',
  pinned_by: alice.id, pinned_by_name: 'Alice', pinned_at: '2026-10-17T10:00:00Z', created_at: '2026-10-17T09:00:00Z',
})
export const outcome = memoryItem({ id: 'note-outcome001', kind: 'outcome', source: 'report', title: 'success: Fixed the reattach race', run_id: 'run_1', harness: 'codex', created_at: '2026-10-18T11:00:00Z' })
export const answer = memoryItem({ id: 'note-answer0001', kind: 'decision', source: 'answer', title: 'Q: Which flow? A: the guest flow', source_ref: 'mq-1', created_at: '2026-10-18T08:00:00Z' })
export const replaced = memoryItem({ id: 'note-old0000001', title: 'Use npm in web/.', superseded_by: workspacePin.id, invalid_at: '2026-10-18T09:00:00Z', created_at: '2026-10-10T09:00:00Z' })

export const liveNotes = [agentNote, outcome, workspacePin, answer, myPin]

/** memory.search over the fixtures, applying the filters the server would. */
export function search(params: MemorySearchParams): { items: MemoryItem[] } {
  const items = (params.replaced ? [replaced] : params.all ? [...liveNotes, replaced] : liveNotes)
    .filter((item) => (!params.kind || item.kind === params.kind) && (!params.source || item.source === params.source) && (!params.pinned || item.pinned_at))
  return { items }
}

export function titles(): string[] {
  return within(screen.getByRole('list', { name: 'Notes' })).getAllByRole('article').map((card) => within(card).getByRole('heading').textContent ?? '')
}

export function rowOf(title: string): HTMLElement {
  return screen.getByRole('button', { name: title }).closest('article')!
}

export function showFilter(name: string) {
  fireEvent.click(within(screen.getByRole('radiogroup', { name: 'Show' })).getByRole('radio', { name: new RegExp(`^${name}`) }))
}

/** The footer's current line; a message on its way out is hidden from assistive technology. */
export function footer(): string {
  const band = screen.getAllByRole('status', { hidden: true }).at(-1)!
  return [...band.querySelectorAll('[data-slot=swap] > span:not([aria-hidden])')].map((layer) => layer.textContent).join('')
}
