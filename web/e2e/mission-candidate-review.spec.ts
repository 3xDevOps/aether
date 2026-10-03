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

test('launches a bounded mission, controls a worker, and shows its candidate without a human gate', async ({ page, aether }, testInfo) => {
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
  await page.getByRole('button', { name: 'Create swarm', exact: true }).click()
  const launch = page.getByRole('dialog', { name: 'Launch a swarm' })
  await expect(launch).toBeVisible()
  await launch.getByPlaceholder('What outcome should the integrator coordinate?').fill(missionObjective)
  // The integrator is always interactive; the worker the fixture starts
  // below needs its own headless choice.
  await expect(launch.getByText(/^Integrator · .* · claude · tui$/)).toBeVisible()
  const workerChoice = launch.locator('label').filter({ hasText: '· claude' })
  await workerChoice.getByRole('checkbox').check()
  await workerChoice.getByRole('combobox').click()
  await page.getByRole('option', { name: 'headless', exact: true }).click()
  await launch.getByLabel('Max concurrent attempts').fill('1')
  await launch.getByLabel('Max total attempts').fill('1')
  await launch.getByRole('button', { name: 'Create swarm', exact: true }).click()
  await expect(page.getByText('Swarm created', { exact: true })).toBeVisible()
  const { missions } = await alice.api.rpc<{
    missions: { id: string; objective: string; current_integrator_run_id: string; integrator_generation: number }[]
  }>('mission.list', { workspace_id: workspaceID })
  const mission = missions.find((item) => item.objective === missionObjective)
  if (!mission) throw new Error('browser launch did not create the mission')
  await waitForCoordCLI(mission.current_integrator_run_id, aether.server.dataDir)

  // The only human step: answering a question the integrator chose to ask.
  runCoordCLI<{ question: { id: string } }>(mission.current_integrator_run_id, [
    'mission', 'question', 'ask',
    '--body', 'which checkout flow?',
    '--idempotency-key', 'mission-candidate-ask',
  ])
  const answerBox = page.getByLabel('Answer question 1', { exact: true })
  await expect(answerBox).toBeVisible({ timeout: terminalTimeout })
  await answerBox.fill('the guest checkout flow')
  await page.getByRole('button', { name: 'Answer', exact: true }).click()
  // The stored answer renders under an attribution line once the refetch
  // lands; the member's display name is whatever the fixture registered.
  await expect(page.getByText(/^Answered by /)).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText('the guest checkout flow', { exact: true })).toBeVisible()

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
  // Start accepts every proposed task and moves the mission to active; no
  // human decides the plan.
  const startedMission = runCoordCLI<{ plan: { phase: string } }>(mission.current_integrator_run_id, [
    'mission', 'start',
    '--mission-id', mission.id,
    '--idempotency-key', 'mission-candidate-start',
  ])
  expect(startedMission.plan.phase).toBe('active')
  await expect(page.getByRole('region', { name: 'Mission tasks' })).toBeVisible({ timeout: 30_000 })
  for (const name of ['Approve', 'Request changes', 'Reject']) {
    await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0)
  }

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
  const inspectWorker = async () => {
    const shown = await alice.api.rpc<{
      attempts?: { id: string; takeover_active?: boolean; takeover_member_id?: string }[]
    }>('mission.show', { mission_id: mission.id })
    const attempt = shown.attempts?.find((item) => item.id === started.attempt.id)
    if (!attempt) throw new Error(`mission worker attempt ${started.attempt.id} was not found`)
    return attempt
  }

  // Viewing an owned worker must not hold orchestration; explicit control does.
  const workerURL = new URL(alice.url)
  workerURL.searchParams.set('run', started.attempt.run_id)
  await page.goto(workerURL.toString())
  await expect(page.getByRole('button', { name: 'Open Run Room' })).toBeVisible({ timeout: terminalTimeout })
  await page.getByRole('button', { name: 'Open Run Room' }).click()
  const room = page.getByRole('complementary', { name: 'Run Room' })
  await expect(room).toBeVisible()
  await expect(page.getByRole('group', { name: 'Run viewers', exact: true }).getByRole('img')).toHaveCount(1, { timeout: terminalTimeout })
  const viewing = await inspectWorker()
  expect(viewing.takeover_active ?? false).toBe(false)
  const controls = page.getByRole('group', { name: 'Terminal attachment controls', exact: true })
  await expect(controls.getByText('Nobody', { exact: true })).toBeVisible()
  await controls.getByRole('button', { name: 'Take control', exact: true }).click()
  await expect(controls.getByRole('button', { name: 'Release', exact: true })).toBeVisible({ timeout: 30_000 })
  const controlled = await inspectWorker()
  expect(controlled.takeover_active).toBe(true)
  expect(controlled.takeover_member_id).toBe(aliceID)

  await page.goto(alice.url)
  await surfaces.getByRole('button', { name: 'Missions', exact: true }).click()
  await page.getByRole('main').getByRole('button', { name: missionObjective, exact: false }).click()
  const missionView = page.getByRole('region', { name: 'Mission tasks' })
  await expect(missionView).toBeVisible()
  await expect(missionView).toContainText('Working')
  await page.getByRole('button', { name: 'Release control', exact: true }).click()
  await expect(page.getByText('Human control released')).toBeVisible({ timeout: 30_000 })
  const released = await inspectWorker()
  expect(released.takeover_active ?? false).toBe(false)
  // The mission page stays open from here on: candidate progress must follow
  // the integrator without a reload.
  const candidateReview = page.getByRole('region', { name: 'Candidate review', exact: true })
  await expect(candidateReview).toContainText('No candidate has been prepared yet.', { timeout: terminalTimeout })

  const workerContainerID = execFileSync(
    'docker',
    ['inspect', '--format', '{{.Id}}', runContainer(started.attempt.run_id)],
    { encoding: 'utf8', timeout: 10_000 },
  ).trim()

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
  await expect
    .poll(
      () => execFileSync(
        'docker',
        ['inspect', '--format', '{{.Id}} {{.State.Paused}} {{.State.Running}}', runContainer(started.attempt.run_id)],
        { encoding: 'utf8', timeout: 10_000 },
      ).trim(),
      { timeout: terminalTimeout, intervals: [250, 500, 1_000, 2_000] },
    )
    .toBe(`${workerContainerID} true true`)
  if (!submission) throw new Error('worker report did not create a mission submission')
  runCoordCLI<TaskMutation>(mission.current_integrator_run_id, [
    'task', 'accept-submission',
    '--submission-id', submission.id,
    '--expected-integrator-generation', String(mission.integrator_generation),
    '--expected-accepted-set-version', '0',
    '--idempotency-key', 'mission-candidate-accept-submission',
  ])

  // The integrator prepares the candidate itself; the server fills in the
  // mission and its accepted set from the assignment.
  const prepared = runCoordCLI<{
    candidate: {
      candidate_id: string
      state: string
      mission_id?: string
      mission_accepted_set_version?: number
      submissions: Array<{ workspace_id: string; run_id: string; evidence_ref: string; retained_revision: string }>
    }
  }>(
    mission.current_integrator_run_id,
    ['integration', 'prepare', '--params-file', '-'],
    JSON.stringify({
      target_ref: 'refs/heads/main',
      expected_target_revision: expectedTargetRevision,
      idempotency_key: 'mission-candidate-prepare',
    }),
  ).candidate
  expect(prepared.state).toBe('frozen')
  expect(prepared.mission_id).toBe(mission.id)
  expect(prepared.mission_accepted_set_version).toBe(1)
  expect(prepared.submissions).toEqual([{
    workspace_id: workspaceID,
    run_id: started.attempt.run_id,
    evidence_ref: submission.ref.evidence_ref,
    retained_revision: submission.ref.retained_revision,
  }])

  // The open mission page picks up the candidate, read-only.
  await expect(candidateReview).toContainText(prepared.candidate_id, { timeout: 30_000 })
  await candidateReview.getByRole('button', { name: 'Show full', exact: true }).click()
  await expect(candidateReview.getByRole('heading', { name: 'Candidate details', exact: true })).toBeVisible({ timeout: terminalTimeout })
  await expect(candidateReview).toContainText(`Mission ${mission.id}`)
  for (const name of ['Prepare candidate', 'Run verification', 'Request delivery', 'Approve delivery', 'Deliver candidate']) {
    await expect(candidateReview.getByRole('button', { name, exact: true })).toHaveCount(0)
  }
  await testInfo.attach('mission candidate progress surface', {
    body: await page.screenshot({ fullPage: true }),
    contentType: 'image/png',
  })
})
