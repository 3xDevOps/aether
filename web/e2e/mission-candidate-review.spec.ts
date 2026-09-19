// A real mission-to-candidate path over the server, Git, Docker runtime, gateway,
// and browser. The only agent behavior is a deterministic shell fixture: it is
// explicitly not a vendor-harness or credentialed-agent demonstration.

import { execFileSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import path from 'node:path'
import { expect, test } from './fixtures'
import { runContainer } from './harness/docker'
import { memberID, seedWorkspace } from './harness/setup'

const terminalTimeout = 3 * 60 * 1000

// The coordination CLI is mounted in every real run container. Keeping this
// helper narrow makes the fixture exercise the same socket and admission path
// as an installed integrator/worker skill, without pretending to be one.
function runCoordCLI<T>(runID: string, args: string[], input?: string): T {
  const output = execFileSync(
    'docker',
    ['exec', '-i', runContainer(runID), 'aether-internal', ...args],
    { input, encoding: 'utf8', timeout: 60_000 },
  )
  const envelope = JSON.parse(output) as { ok?: boolean; result?: T; error?: { message?: string } }
  if (!envelope.ok || envelope.result === undefined) {
    throw new Error(envelope.error?.message || `aether-internal ${args.join(' ')} failed`)
  }
  return envelope.result
}

async function waitForCoordCLI(runID: string, dataDir: string): Promise<void> {
  await expect
    .poll(() => existsSync(path.join(dataDir, 'coord', runID, 'coord3.sock')), {
      timeout: 30_000,
      intervals: [100, 250, 500],
    })
    .toBeTruthy()
}

test('launches a bounded mission, controls a worker, and prepares its accepted candidate', async ({ page, aether }, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('mission-candidate-project')
  const expectedTargetRevision = await seedWorkspace(alice, aether.server.addr, repo, 'mission-candidate-project')
  const aliceID = await memberID(alice)
  aether.installAgent(aliceID, 'claude', 'printf "scripted mission fixture agent\\n"\nsleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspaceID = workspaces[0]?.id
  if (!workspaceID) throw new Error('mission fixture workspace was not created')

  const missionObjective = 'mission candidate browser fixture'
  await page.goto(alice.url)
  const surfaces = page.getByRole('navigation', { name: 'Surfaces' })
  await surfaces.getByRole('button', { name: 'Missions', exact: true }).click()
  await page.getByRole('button', { name: 'Launch swarm', exact: true }).click()
  const launch = page.getByRole('dialog', { name: 'Launch a swarm' })
  await expect(launch).toBeVisible()
  await launch.getByPlaceholder('What outcome should the integrator coordinate?').fill(missionObjective)
  await launch.getByLabel('Integrator mode', { exact: true }).click()
  await page.getByRole('option', { name: 'Headless', exact: true }).click()
  const workerChoice = launch.locator('label').filter({ hasText: '· claude' })
  await workerChoice.getByRole('checkbox').check()
  await workerChoice.getByRole('combobox').click()
  await page.getByRole('option', { name: 'headless', exact: true }).click()
  await launch.getByLabel('Max concurrent attempts').fill('1')
  await launch.getByLabel('Max total attempts').fill('1')
  await launch.getByRole('button', { name: 'Launch Swarm', exact: true }).click()
  await expect(page.getByText('Swarm launched', { exact: true })).toBeVisible()
  const { missions } = await alice.api.rpc<{
    missions: { id: string; objective: string; current_integrator_run_id: string; integrator_generation: number }[]
  }>('mission.list', { workspace_id: workspaceID })
  const mission = missions.find((item) => item.objective === missionObjective)
  if (!mission) throw new Error('browser launch did not create the mission')
  await waitForCoordCLI(mission.current_integrator_run_id, aether.server.dataDir)

  type TaskMutation = { task: { id: string; current_revision: number } }
  const proposed = runCoordCLI<TaskMutation>(
    mission.current_integrator_run_id,
    ['task', 'propose', '--mission-id', mission.id, '--idempotency-key', 'mission-candidate-propose', '--revision-file', '-'],
    JSON.stringify({
      title: 'scripted worker candidate',
      objective: 'Produce the accepted candidate fixture output.',
      status: 'proposed',
      scope: {},
      evidence_requirements: [],
    }),
  )
  runCoordCLI<TaskMutation>(mission.current_integrator_run_id, [
    'task', 'accept',
    '--task-id', proposed.task.id,
    '--revision', String(proposed.task.current_revision),
    '--expected-integrator-generation', String(mission.integrator_generation),
    '--idempotency-key', 'mission-candidate-accept-task',
  ])
  const started = runCoordCLI<{ attempt: { id: string; run_id: string } }>(mission.current_integrator_run_id, [
    'worker', 'start',
    '--mission-id', mission.id,
    '--task-id', proposed.task.id,
    '--task-revision', String(proposed.task.current_revision),
    '--dispatch-key', 'mission-candidate-worker',
    '--harness', 'claude',
    '--mode', 'headless',
    '--account-owner-id', aliceID,
    '--run-owner-id', aliceID,
    '--expected-integrator-generation', String(mission.integrator_generation),
  ])
  await waitForCoordCLI(started.attempt.run_id, aether.server.dataDir)

  // Browser-visible worker control: the human takes and then releases the
  // active worker hold before the fixture submits its result.
  const workerURL = new URL(alice.url)
  workerURL.searchParams.set('run', started.attempt.run_id)
  await page.goto(workerURL.toString())
  await expect(page.getByText('Attached', { exact: true })).toBeVisible({ timeout: terminalTimeout })
  await expect(page.getByRole('button', { name: 'Open Run Room' })).toBeVisible({ timeout: terminalTimeout })
  await page.getByRole('button', { name: 'Open Run Room' }).click()
  const room = page.getByRole('complementary', { name: 'Run Room' })
  await expect(room).toBeVisible()
  // The owner's initial attach already holds control.
  await room.getByRole('button', { name: 'Release control', exact: true }).click()
  await room.getByRole('button', { name: 'Take control', exact: true }).click()
  await expect(room.getByRole('button', { name: 'Release control', exact: true })).toBeVisible({ timeout: 30_000 })

  await page.goto(alice.url)
  await surfaces.getByRole('button', { name: 'Missions', exact: true }).click()
  await page.getByRole('main').getByRole('button', { name: missionObjective, exact: false }).click()
  const missionView = page.getByRole('region', { name: 'Mission tasks' })
  await expect(missionView).toBeVisible()
  await expect(missionView).toContainText('Working')
  await page.getByRole('button', { name: 'Release control', exact: true }).click()
  await expect(page.getByText('Human control released')).toBeVisible({ timeout: 30_000 })

  // The real worker reports through its mounted CLI. Reconciliation creates
  // the proposed submission; the integrator fixture then accepts it with the
  // current mission set version.
  runCoordCLI<{ report_id: string }>(started.attempt.run_id, [
    'report', '--outcome', 'success', '--summary', 'scripted fixture completed', '--idempotency-key', 'mission-candidate-report',
  ])
  type Submission = { id: string; state: string; ref: { evidence_ref: string; retained_revision: string; run_id: string } }
  let submission: Submission | undefined
  await expect
    .poll(
      async () => {
        const shown = await alice.api.rpc<{ submissions?: Submission[] }>('mission.show', { mission_id: mission.id })
        submission = shown.submissions?.find((item) => item.ref.run_id === started.attempt.run_id)
        return submission?.state ?? ''
      },
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe('proposed')
  if (!submission) throw new Error('worker report did not create a mission submission')
  runCoordCLI<TaskMutation>(mission.current_integrator_run_id, [
    'task', 'accept-submission',
    '--submission-id', submission.id,
    '--expected-integrator-generation', String(mission.integrator_generation),
    '--expected-accepted-set-version', '0',
    '--idempotency-key', 'mission-candidate-accept-submission',
  ])

  const missionURL = new URL(alice.url)
  await page.goto(missionURL.toString())
  await surfaces.getByRole('button', { name: 'Missions', exact: true }).click()
  await page.getByRole('main').getByRole('button', { name: missionObjective, exact: false }).click()
  const candidateReview = page.getByRole('region', { name: 'Candidate review', exact: true })
  await expect(candidateReview).toBeVisible({ timeout: terminalTimeout })
  await expect(candidateReview.getByTestId('mission-candidate-inputs')).toContainText(submission.ref.evidence_ref)
  await expect(candidateReview.getByLabel('Target ref')).toHaveValue('refs/heads/main')
  await expect(candidateReview.getByLabel('Expected target revision')).toHaveValue(expectedTargetRevision)
  await candidateReview.getByRole('button', { name: 'Prepare candidate', exact: true }).click()
  await expect(candidateReview.getByRole('heading', { name: 'Candidate details', exact: true })).toBeVisible({ timeout: terminalTimeout })
  const listedCandidates = await alice.api.rpc<{ candidates: { candidate_id: string }[] }>('integration.list', {
    workspace_id: workspaceID,
    limit: 10,
  })
  const candidateID = listedCandidates.candidates[0]?.candidate_id
  if (!candidateID) throw new Error('mission candidate prepare returned no candidate')
  const prepared = await alice.api.rpc<{
    candidate: {
      state: string
      mission_id?: string
      mission_accepted_set_version?: number
      submissions: Array<{ workspace_id: string; run_id: string; evidence_ref: string; retained_revision: string }>
    }
  }>('integration.show', { workspace_id: workspaceID, candidate_id: candidateID })
  expect(prepared.candidate.state).toBe('frozen')
  expect(prepared.candidate.mission_id).toBe(mission.id)
  expect(prepared.candidate.mission_accepted_set_version).toBe(1)
  expect(prepared.candidate.submissions).toEqual([{
    workspace_id: workspaceID,
    run_id: started.attempt.run_id,
    evidence_ref: submission.ref.evidence_ref,
    retained_revision: submission.ref.retained_revision,
  }])
  await expect(candidateReview).toContainText(`Mission ${mission.id}`)
  await testInfo.attach('mission accepted candidate review surface', {
    body: await page.screenshot({ fullPage: true }),
    contentType: 'image/png',
  })
})
