// A swarm on a phone: the list card, the detail's body heading, a question
// answered from its own card and agent messages from a real worker, with no
// sideways scroll. The integrator is a shell fixture, not a vendor agent.

import { runCoordCLI, waitForCoordCLI } from './harness/coord'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

const launchTimeout = 3 * 60 * 1000

test('reads and answers a swarm on a phone', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('swarm-phone-project')
  await seedWorkspace(alice, aether.server.addr, repo, 'swarm-phone-project')
  const aliceID = await memberID(alice)
  aether.installAgent(aliceID, 'claude', 'printf "scripted swarm fixture agent\\n"\nsleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspaceID = workspaces[0].id

  const objective = 'Swarm phone fixture: port checkout to the payments API'
  const integrator = { account_member_id: aliceID, harness: 'claude', mode: 'tui' }
  const { mission } = await alice.api.rpc<{ mission: { id: string; current_integrator_run_id: string; integrator_generation: number } }>(
    'mission.create',
    {
      workspace_id: workspaceID,
      objective,
      accountable_human_id: aliceID,
      integrator,
      execution_choices: [{ ...integrator, mode: 'headless' }, integrator],
      idempotency_key: 'swarm-phone-create',
    },
  )
  const integratorRun = mission.current_integrator_run_id
  await waitForCoordCLI(integratorRun, aether.server.dataDir)
  runCoordCLI(integratorRun, ['mission', 'question', 'ask', '--body', 'Keep the legacy receipt template?', '--idempotency-key', 'swarm-phone-ask'])

  const url = new URL(alice.url)
  url.searchParams.set('page', 'missions')
  await page.goto(url.toString())
  const card = page.getByRole('list', { name: 'Swarms' }).getByRole('article')
  await expect(card).toContainText('The integrator has a question', { timeout: launchTimeout })
  await expect(card).toContainText(objective)
  await card.getByRole('button', { name: objective }).click()

  const overview = page.getByRole('region', { name: 'Swarm' })
  await expect(overview.getByText(objective, { exact: true })).toBeVisible()
  await expect(overview).toContainText('Planning · 1 question for you')
  const questions = page.getByRole('region', { name: 'Questions for you' })
  await questions.getByLabel('Answer question 1').fill('Switch to the new template')
  await questions.getByRole('button', { name: 'Answer', exact: true }).click()
  const answeredQuestions = page.getByRole('region', { name: 'Questions from the integrator' })
  await expect(answeredQuestions.getByText(/^Answered by /)).toBeVisible({ timeout: 30_000 })

  type TaskMutation = { task: { id: string; current_revision: number } }
  const proposed = runCoordCLI<TaskMutation>(
    integratorRun,
    ['task', 'propose', '--mission-id', mission.id, '--idempotency-key', 'swarm-phone-propose', '--revision-file', '-'],
    JSON.stringify({ title: 'Port the guest checkout controller', objective: 'Use payments.charge().', status: 'proposed', scope: {}, evidence_requirements: [] }),
  )
  runCoordCLI(integratorRun, ['mission', 'start', '--mission-id', mission.id, '--idempotency-key', 'swarm-phone-start'])
  const started = runCoordCLI<{ attempt: { run_id: string } }>(integratorRun, [
    'worker', 'start', '--mission-id', mission.id, '--task-id', proposed.task.id,
    '--task-revision', String(proposed.task.current_revision), '--dispatch-key', 'swarm-phone-worker',
    '--harness', 'claude', '--mode', 'headless', '--account-owner-id', aliceID, '--run-owner-id', aliceID,
    '--expected-integrator-generation', String(mission.integrator_generation),
  ])
  await waitForCoordCLI(started.attempt.run_id, aether.server.dataDir)
  for (const [key, body] of [['a', 'Started on the controller.'], ['b', 'Found two call sites.']]) {
    runCoordCLI(started.attempt.run_id, ['send', '--to', integratorRun, '--body', body, '--idempotency-key', `swarm-phone-${key}`])
  }

  const tasks = page.getByRole('region', { name: 'Tasks' })
  await expect(tasks).toContainText('Port the guest checkout controller', { timeout: 30_000 })
  const messages = page.getByRole('region', { name: 'Agent messages' })
  await expect(messages.getByRole('button', { name: '2 messages' })).toBeVisible({ timeout: 30_000 })
  await messages.getByRole('button', { name: '2 messages' }).click()
  await expect(messages).toContainText('Found two call sites.')

  const overflow = await page.evaluate(() => {
    const scroller = document.querySelector('[aria-label="Swarm"]')?.closest('.overflow-y-auto')
    return scroller ? scroller.scrollWidth - scroller.clientWidth : -1
  })
  expect(overflow).toBe(0)
  const answered = answeredQuestions.getByRole('button', { name: /Keep the legacy receipt template\?/ })
  expect((await answered.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(44)
})
