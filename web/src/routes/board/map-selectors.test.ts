import { act, renderHook } from '@testing-library/react'
import { board, mapRuns, useMapRuns } from '@/routes/board/selectors'
import { useStore } from '@/store'
import { alice, bob, otherWorkspace, run, runRecords, stateContext, workspace } from '@/test/fixtures'
import { toRecord } from '@/store/runs'

it('exposes each run of a family while Board retains one triage card', () => {
  const ctx = stateContext({
    runs: runRecords(
      run({ id: 'run_parent' }),
      run({ id: 'child', member_id: bob.id, parent_run_id: 'run_parent' }),
      run({ id: 'missing-parent-child', parent_run_id: 'hidden' }),
    ),
  })
  const input = { workspace: workspace.id, mineOnly: false, ctx }
  const cards = mapRuns(input).cards
  expect(cards.map((card) => card.run.id).sort()).toEqual(['child', 'missing-parent-child', 'run_parent'])
  expect(cards.every((card) => !card.childRuns && card.children.length === 0)).toBe(true)
  expect(board(input).columns.flatMap((column) => column.cards).map((card) => card.run.id).sort()).toEqual(['missing-parent-child', 'run_parent'])
})

it('keeps Needs you global while workspace and Mine filter individual working and finished runs', () => {
  const ctx = stateContext({ runs: runRecords(
    run({ id: 'mine' }),
    run({ id: 'teammate', member_id: bob.id }),
    run({ id: 'elsewhere', workspace_id: otherWorkspace.id }),
    run({ id: 'needs-you-elsewhere', status: 'needs-attention', state: 'needs-you', workspace_id: otherWorkspace.id }),
    run({ id: 'archived', status: 'merged', archived_at: '2026-10-01T00:00:00Z' }),
    run({ id: 'archived-teammate', member_id: bob.id, status: 'merged', archived_at: '2026-10-01T00:00:00Z' }),
    run({ id: 'live-with-archive', archived_at: '2026-10-01T00:00:00Z' }),
  ) })
  const filtered = mapRuns({ workspace: workspace.id, mineOnly: true, ctx })
  expect(filtered.cards.map((card) => card.run.id).sort()).toEqual(['live-with-archive', 'mine', 'needs-you-elsewhere'])
  expect(filtered.cards.find((card) => card.run.id === 'needs-you-elsewhere')?.workspaceName).toBe(otherWorkspace.name)
  expect(filtered.cards.filter((card) => card.visiting).map((card) => card.run.id)).toEqual(['needs-you-elsewhere'])
  expect(filtered.archivedCards.map((card) => card.run.id)).toEqual(['archived'])
  const all = mapRuns({ workspace: '', mineOnly: false, ctx })
  expect(all.cards.map((card) => card.run.id).sort()).toEqual(['elsewhere', 'live-with-archive', 'mine', 'needs-you-elsewhere', 'teammate'])
  expect(all.cards.find((card) => card.run.member_id === alice.id)?.workspaceName).toBe(workspace.name)
  expect(all.archivedCards.map((card) => card.run.id).sort()).toEqual(['archived', 'archived-teammate'])
})

it('keeps an unchanged card object when another run changes, so only that card re-renders', () => {
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    members: { [alice.id]: alice },
    runs: { quiet: toRecord(run({ id: 'quiet' })), busy: toRecord(run({ id: 'busy' })) },
  })
  const { result } = renderHook(() => useMapRuns(workspace.id))
  const before = new Map(result.current.cards.map((card) => [card.run.id, card]))
  act(() => useStore.setState((s) => ({ runs: { ...s.runs, busy: { ...s.runs.busy, title: 'Reading main.go' } } })))
  const after = new Map(result.current.cards.map((card) => [card.run.id, card]))
  expect(after.get('quiet')).toBe(before.get('quiet'))
  expect(after.get('busy')).not.toBe(before.get('busy'))
})
