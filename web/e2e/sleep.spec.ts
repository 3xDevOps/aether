// An idle Enhanced Claude Code session against a real server with a short
// --run-sleep-after: its container stops, the Chat view says since when it
// sleeps, and a message or Wake starts it again into the same conversation.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

test.use({ colorScheme: 'dark', serverOptions: { args: ['--run-sleep-after', '3s', '--poll-interval', '1s'] } })

test('an idle session falls asleep, shows since when, and Wake brings it back', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installClaudeACPMock(await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0].id, harness: 'claude', mode: 'acp', task: 'demo' })
  const read = async () => (await alice.api.rpc<{ run: { status: string; asleep_since?: string; waking?: boolean } }>('run.get', { run_id: run.id })).run

  await page.goto(`${alice.url}&run=${run.id}`)
  const log = page.getByRole('log', { name: 'Chat' })
  const answer = log.getByText('The totals now round to cents before they are returned.')
  await expect(answer).toBeVisible({ timeout: 60_000 })

  await expect.poll(async () => (await read()).asleep_since, { timeout: 60_000 }).toBeTruthy()
  // The Terminal view stays mounted, hidden, with its own copy of the callout.
  const callout = page.locator('[data-slot="callout"]').filter({ hasText: 'Asleep since', visible: true })
  await expect(callout).toBeVisible()
  await expect(callout.getByRole('button', { name: 'Wake' })).toBeEnabled()
  for (const [name, width] of [['desktop', 1440], ['430', 430], ['360', 360]] as const) {
    await page.setViewportSize({ width, height: 900 })
    await page.screenshot({ path: testInfo.outputPath(`asleep-${name}.png`), animations: 'disabled' })
  }
  await page.setViewportSize({ width: 1440, height: 900 })

  // A message wakes the session, and the agent answers it in the same conversation.
  const box = page.getByRole('combobox', { name: 'Message the agent' })
  await box.fill('demo')
  await box.press('Enter')
  await expect(answer).toHaveCount(2, { timeout: 60_000 })
  await expect(log.getByText('Woke from sleep')).toBeVisible()
  expect((await read()).status).not.toMatch(/completed|failed|abandoned|interrupted/)

  // Idle again, it falls asleep again, and Wake brings it back.
  await expect.poll(async () => (await read()).asleep_since, { timeout: 60_000 }).toBeTruthy()
  await callout.getByRole('button', { name: 'Wake' }).click()
  await expect(page.locator('[data-slot="callout"]').filter({ hasText: 'Waking…', visible: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('waking-desktop.png'), animations: 'disabled' })
  await expect.poll(async () => (await read()).asleep_since === undefined || undefined, { timeout: 60_000 }).toBe(true)
})
