import { board, mapRuns } from '@/routes/board/selectors'
import { alice, bob, mission, otherWorkspace, run, runRecords, stateContext, workspace } from '@/test/fixtures'

it('exposes each swarm run while Board retains one triage card', () => {
  const ctx = stateContext({
    missions: { mission_1: mission() },
    runs: runRecords(
      run({ id: 'run_integrator', mission_id: 'mission_1', mission_role: 'integrator' }),
      run({ id: 'worker', member_id: bob.id, mission_id: 'mission_1', mission_role: 'worker', integrator_run_id: 'run_integrator' }),
      run({ id: 'missing-root-worker', mission_id: 'mission_2', mission_role: 'worker', integrator_run_id: 'hidden' }),
    ),
  })
  const input = { workspace: workspace.id, mineOnly: false, ctx }
  const cards = mapRuns(input).cards
  expect(cards.map((card) => card.run.id).sort()).toEqual(['missing-root-worker', 'run_integrator', 'worker'])
  expect(cards.every((card) => !card.swarm && card.children.length === 0)).toBe(true)
  expect(board(input).columns.flatMap((column) => column.cards).map((card) => card.run.id).sort()).toEqual(['missing-root-worker', 'run_integrator'])
})

it('keeps Needs you global while workspace and Mine filter individual working and finished runs', () => {
  const ctx = stateContext({ runs: runRecords(
    run({ id: 'mine' }),
    run({ id: 'teammate', member_id: bob.id }),
    run({ id: 'elsewhere', workspace_id: otherWorkspace.id }),
    run({ id: 'needs-you-elsewhere', status: 'needs-attention', workspace_id: otherWorkspace.id }),
    run({ id: 'archived', status: 'merged', archived_at: '2026-10-01T00:00:00Z' }),
    run({ id: 'archived-teammate', member_id: bob.id, status: 'merged', archived_at: '2026-10-01T00:00:00Z' }),
    run({ id: 'live-with-archive', archived_at: '2026-10-01T00:00:00Z' }),
  ) })
  const filtered = mapRuns({ workspace: workspace.id, mineOnly: true, ctx })
  expect(filtered.cards.map((card) => card.run.id).sort()).toEqual(['live-with-archive', 'mine', 'needs-you-elsewhere'])
  expect(filtered.cards.find((card) => card.run.id === 'needs-you-elsewhere')?.workspaceName).toBe(otherWorkspace.name)
  expect(filtered.archivedCards.map((card) => card.run.id)).toEqual(['archived'])
  const all = mapRuns({ workspace: '', mineOnly: false, ctx })
  expect(all.cards.map((card) => card.run.id).sort()).toEqual(['elsewhere', 'live-with-archive', 'mine', 'needs-you-elsewhere', 'teammate'])
  expect(all.cards.find((card) => card.run.member_id === alice.id)?.workspaceName).toBe(workspace.name)
  expect(all.archivedCards.map((card) => card.run.id).sort()).toEqual(['archived', 'archived-teammate'])
})
