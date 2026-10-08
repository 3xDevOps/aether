// An Enhanced run's Session view against a real server: the acpmock agent
// runs as the run's ACP adapter, the dashboard streams /ws/acp, answers a
// permission natively, sends a message, queues one behind a turn and interrupts it.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('an Enhanced run streams, answers a permission, takes a message and stops on Interrupt', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'mock',
    mode: 'acp',
    task: 'demo',
  })

  await page.goto(`${alice.url}&run=${run.id}`)
  const log = page.getByRole('log', { name: 'Session' })
  await expect(log.getByText('pong')).toBeVisible({ timeout: 60_000 })
  const box = page.getByRole('combobox', { name: 'Message the agent' })

  await box.fill('ask permission')
  await box.press('ControlOrMeta+Enter')
  const card = page.locator('#session-request-docked')
  await expect(card.getByText('rm -rf build')).toBeVisible()
  await expect(page.getByText('Answer the request above to continue.')).toBeVisible()
  await page.getByRole('toolbar', { name: 'Run actions' }).getByRole('button', { name: 'Answer' }).click()
  await expect(card).toBeFocused()
  await expect(card.getByRole('button')).toHaveText(['Allow', 'Reject', 'Always Allow'])
  await expect(page.getByRole('log', { name: 'Session' }).getByText('Waiting for your approval: rm -rf build')).toBeVisible()
  await page.keyboard.press('1')
  await expect(log.getByText(/Approved: run/)).toBeVisible()
  await expect(log.getByText('permission: allow')).toBeVisible()
  await expect.poll(async () => {
    const [row, view] = await Promise.all([log.getByText('permission: allow').boundingBox(), log.boundingBox()])
    return Boolean(row && view && row.y >= view.y && row.y + row.height <= view.y + view.height)
  }, { message: 'the answered reply sits inside the scrolled log' }).toBe(true)

  await box.fill('ask permission')
  await box.press('ControlOrMeta+Enter')
  await expect(card.getByText('rm -rf build')).toBeVisible()
  await expect(card).not.toBeFocused()
  await card.getByRole('button', { name: 'Allow', exact: true }).click()
  await expect(log.getByText('permission: allow', { exact: true })).toHaveCount(2)
  await page.screenshot({ path: testInfo.outputPath('enhanced-first-click-approval.png') })

  await box.fill('demo')
  await box.press('ControlOrMeta+Enter')
  await expect(log.getByRole('button', { name: /Read 2 files, ran 1 command and edited 1 file/ })).toBeVisible({ timeout: 30_000 })
  await expect(log.getByText('Changed 1 file')).toBeVisible()
  await log.getByRole('button', { name: /Read 2 files, ran 1 command and edited 1 file/ }).click()
  await log.getByRole('button', { name: /Edited src\/billing\.js/ }).click()
  await expect(log.locator('[data-slot="diff-block"]')).toContainText('Math.round')
  // The mock advertises steering but does not implement it, so wait for the turn to end.
  await expect(log.getByText(/^Finished in \d+s$/)).toBeVisible()

  await box.fill('wait')
  await box.press('ControlOrMeta+Enter')
  const interrupt = page.getByRole('button', { name: 'Interrupt the agent' })
  await expect(interrupt).toBeVisible()
  await box.fill('after the wait')
  await box.press('ControlOrMeta+Shift+Enter')
  const queued = log.locator('[data-slot="message-row"]', { hasText: 'after the wait' })
  await expect(queued).toContainText('Queued')
  await interrupt.click()
  await expect(log.getByText(/^Interrupted/)).toBeVisible()
  await expect(queued).not.toContainText('Queued')
  await expect.poll(async () => {
    const { messages } = await alice.api.rpc<{ messages: { body: string; state: string; agent_delivery?: string }[] }>(
      'run.room.list', { workspace_id: workspaces[0].id, run_id: run.id },
    )
    const sent = messages.find((m) => m.body === 'after the wait')
    return sent && `${sent.state}/${sent.agent_delivery}`
  }, { message: 'the queued message is recorded as delivered' }).toBe('sent/delivered')
  await expect(page.getByRole('button', { name: 'Send' })).toBeVisible()
})
