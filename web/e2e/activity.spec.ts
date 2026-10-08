// Activity reads the real workspace log and the real agent-message history,
// and its one Filter popover is a Radix popover holding Radix selects, which
// only a real browser stacks and dismisses the way a person meets them.

import { execFileSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import path from 'node:path'
import { expect, test } from './fixtures'
import { runContainer } from './harness/docker'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the run that writes the log needs a reachable Docker daemon')

test('Activity narrows the log from one filter and switches to agent messages', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  await alice.api.rpc('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    mode: 'headless',
    task: 'write the activity fixture',
  })

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Activity', exact: true }).click()
  const feed = page.getByRole('region', { name: 'Activity feed' })
  const runState = /Run queued|Run starting|Agent working|Needs you|Finished|Failed|Stopped/
  await expect(feed.getByRole('listitem').filter({ hasText: 'Agent working' }).first()).toBeVisible({ timeout: 120_000 })
  await expect(feed.getByText(/run\.status|needs-attention/)).toHaveCount(0)

  await page.getByRole('button', { name: 'Filter', exact: true }).click()
  const filter = page.getByRole('dialog')
  await filter.getByLabel('Show').click()
  await page.getByRole('option', { name: 'Run state' }).click()
  await expect(page.getByRole('button', { name: 'Filter · 1' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(filter).toBeHidden()
  const rows = feed.getByRole('listitem')
  await expect(rows.first()).toContainText(runState)
  for (const text of await rows.allTextContents()) expect(text).toMatch(runState)

  await page.getByRole('button', { name: 'More activity options' }).click()
  await page.getByRole('menuitemcheckbox', { name: 'Raw events' }).click()
  await expect(rows.first()).toContainText('run.status')
  await expect(rows.first()).toContainText('"to":')

  await page.getByRole('button', { name: 'Filter · 1' }).click()
  await filter.getByLabel('Show').click()
  await page.getByRole('option', { name: 'Agent messages' }).click()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('searchbox', { name: 'Search agent messages' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'No agent messages yet' })).toBeVisible()
  await expect(page.getByText(/coord\.messages\.list/)).toHaveCount(0)
})

function coord<T>(runID: string, args: string[]): T {
  const output = execFileSync('docker', ['exec', runContainer(runID), 'aether-internal', ...args], { encoding: 'utf8', timeout: 60_000 })
  const envelope = JSON.parse(output) as { ok?: boolean; result?: T; error?: { message?: string } }
  if (!envelope.ok || envelope.result === undefined) throw new Error(envelope.error?.message || `aether-internal ${args.join(' ')} failed`)
  return envelope.result
}

test('Activity shows real agent messages and narrows them to one thread', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const aliceID = await memberID(alice)
  aether.installAgent(aliceID, 'claude', 'sleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const choice = (mode: string) => ({ account_member_id: aliceID, harness: 'claude', mode })
  const { mission } = await alice.api.rpc<{ mission: { id: string; current_integrator_run_id: string; integrator_generation: number } }>('mission.create', {
    idempotency_key: 'activity-messages',
    workspace_id: workspaces[0].id,
    objective: 'activity message fixture',
    accountable_human_id: aliceID,
    integrator: choice('tui'),
    execution_choices: [choice('tui'), choice('headless')],
  })
  const lead = mission.current_integrator_run_id
  const socket = (runID: string) => expect.poll(() => existsSync(path.join(aether.server.dataDir, 'coord', runID, 'coord3.sock')), { timeout: 120_000 }).toBeTruthy()
  await socket(lead)
  const { task } = coord<{ task: { id: string; current_revision: number } }>(lead, [
    'task', 'propose', '--mission-id', mission.id, '--idempotency-key', 'activity-task',
    '--revision', JSON.stringify({ title: 'messages', objective: 'Answer one question.', status: 'proposed', scope: {}, evidence_requirements: [] }),
  ])
  coord(lead, ['mission', 'start', '--mission-id', mission.id, '--idempotency-key', 'activity-start'])
  const worker = coord<{ attempt: { run_id: string } }>(lead, [
    'worker', 'start', '--mission-id', mission.id, '--task-id', task.id, '--task-revision', String(task.current_revision),
    '--dispatch-key', 'activity-worker', '--harness', 'claude', '--mode', 'headless',
    '--account-owner-id', aliceID, '--run-owner-id', aliceID,
    '--expected-integrator-generation', String(mission.integrator_generation),
  ]).attempt.run_id
  await socket(worker)
  coord(lead, ['send', '--to', worker, '--idempotency-key', 'activity-send', '--body', 'Start with the parser.'])
  const { question_id: question } = coord<{ question_id: string }>(worker, ['ask', '--to', lead, '--idempotency-key', 'activity-ask', '--body', 'Which parser?'])
  coord(lead, ['reply', '--question-id', question, '--idempotency-key', 'activity-reply', '--body', 'The JSON one.'])

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Activity', exact: true }).click()
  await page.getByRole('button', { name: 'Filter', exact: true }).click()
  await page.getByRole('dialog').getByLabel('Show').click()
  await page.getByRole('option', { name: 'Agent messages' }).click()
  await page.keyboard.press('Escape')

  const history = page.getByRole('region', { name: 'Agent messages' })
  const ask = history.getByRole('article', { name: `Question ${question}` })
  await expect(ask).toBeVisible({ timeout: 30_000 })
  await expect(ask).toContainText('Which parser?')
  await expect(ask.getByRole('img', { name: 'to' })).toBeVisible()
  await expect(ask).toContainText(/Sent|Delivered|Acknowledged/)
  await expect(history.getByRole('article')).toHaveCount(3)

  await page.getByRole('searchbox', { name: 'Search agent messages' }).fill('which')
  await expect(history.getByRole('article')).toHaveCount(1)
  await page.getByRole('searchbox', { name: 'Search agent messages' }).fill('')

  await ask.getByRole('button', { name: 'Thread', exact: true }).click()
  await expect(history.getByRole('article')).toHaveCount(2)
  await expect(history).toContainText('The JSON one.')
  await expect(history).not.toContainText('Start with the parser.')
})
