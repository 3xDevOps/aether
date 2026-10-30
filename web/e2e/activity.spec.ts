// Activity reads the real workspace log and the real agent-message history,
// and its one Filter popover is a Radix popover holding Radix selects, which
// only a real browser stacks and dismisses the way a person meets them.

import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { expect, test } from './fixtures'
import { runCoordCLI, waitForCoordCLI } from './harness/coord'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the session that writes the log needs a reachable Docker daemon')

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
  const runState = /Run queued|Session starting|Working|Waiting for agents|Waiting for CI|Waiting for process|Waiting|Needs you|Ready for review|Completed|Failed|Disconnected|Stopped/
  await expect(feed.getByRole('listitem').filter({ hasText: 'Working' }).first()).toBeVisible({ timeout: 120_000 })
  await expect(feed.getByText(/run\.status|needs-attention/)).toHaveCount(0)

  await page.getByRole('button', { name: 'Filter', exact: true }).click()
  const filter = page.getByRole('dialog')
  await filter.getByLabel('Show').click()
  await page.getByRole('option', { name: 'Session state' }).click()
  await expect(page.getByRole('button', { name: 'Filter, 1 active' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(filter).toBeHidden()
  const rows = feed.getByRole('listitem')
  await expect(rows.first()).toContainText(runState)
  for (const text of await rows.allTextContents()) expect(text).toMatch(runState)

  await page.getByRole('button', { name: 'More activity options' }).click()
  await page.getByRole('menuitemcheckbox', { name: 'Raw events' }).click()
  await expect(rows.first()).toContainText('run.status')
  await expect(rows.first()).toContainText('"to":')

  await page.getByRole('button', { name: 'Filter, 1 active' }).click()
  await filter.getByLabel('Show').click()
  await page.getByRole('option', { name: 'Agent messages' }).click()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('searchbox', { name: 'Search agent messages' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'No agent messages yet' })).toBeVisible()
  await expect(page.getByText(/coord\.messages\.list/)).toHaveCount(0)
})

test('Activity shows real agent messages and narrows them to one thread', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const aliceID = await memberID(alice)
  aether.installAgent(aliceID, 'claude', 'sleep 600')
  aether.installAgent(aliceID, 'codex', 'sleep 600')
  const claudeHome = path.join(aether.server.memberHome(aliceID), '.claude')
  mkdirSync(claudeHome, { recursive: true })
  writeFileSync(path.join(claudeHome, '.credentials.json'), '{}\n')
  aether.giveCodexLogin(aliceID)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run: parent } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id, task: 'activity message fixture', harness: 'claude', mode: 'tui',
  })
  await waitForCoordCLI(parent.id, aether.server.dataDir)
  const child = runCoordCLI<{ run_id: string }>(parent.id, [
    'child', 'start', '--harness', 'codex', '--mode', 'standard', '--purpose', 'Answer one question',
    '--task', 'Answer one question.', '--idempotency-key', 'activity-child',
  ]).run_id
  await waitForCoordCLI(child, aether.server.dataDir)
  runCoordCLI(parent.id, ['send', '--to', child, '--idempotency-key', 'activity-send', '--body', 'Start with the parser.'])
  const { question_id: question } = runCoordCLI<{ question_id: string }>(child, ['ask', '--to', parent.id, '--idempotency-key', 'activity-ask', '--body', 'Which parser?'])
  runCoordCLI(parent.id, ['reply', '--question-id', question, '--idempotency-key', 'activity-reply', '--body', 'The JSON one.'])

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
