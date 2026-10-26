// A watch against a real server: created from a run's menu, it keeps
// evaluating with every dashboard page closed, and the notice it records
// while nobody is looking shows on the next visit with where it was delivered.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

interface WatchGet {
  watch: { id: string; state: string }
  notifications: { condition: string; deliveries: { channel: string; status: string }[] }[]
}

test('a watch made from the run menu notices a need while the page is closed', async ({ page, aether }, testInfo) => {
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
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))

  await page.goto(`${alice.url}&run=${run.id}`)
  await expect(page.getByRole('log', { name: 'Session' }).getByText('pong')).toBeVisible({ timeout: 60_000 })
  await page.getByRole('toolbar', { name: 'Run actions' }).getByRole('button', { name: 'More', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Watch…' }).click()
  const form = page.getByRole('dialog', { name: /^Watch / })
  await expect(form.getByRole('checkbox', { name: 'Needs you', exact: true })).toBeChecked()
  await expect(form.getByRole('checkbox', { name: 'Finished', exact: true })).toBeChecked()
  await expect(form.getByText(/Finished means the agent reported done/)).toBeVisible()
  await form.getByRole('button', { name: 'Watch', exact: true }).click()
  await expect(form).toBeHidden()
  await expect(page.getByText(/^Watching run .* for: needs you, finished\./)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Watching' })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('watch-created.png') })

  const { watches } = await alice.api.rpc<{ watches: { id: string }[] }>('watch.list', { run_id: run.id })
  expect(watches).toHaveLength(1)
  const watchID = watches[0].id

  // Nobody has a dashboard open while the agent asks for a permission.
  await page.goto('about:blank')
  await alice.api.rpc('run.inject', { run_id: run.id, message: 'ask permission', idempotency_key: `watch-e2e-${run.id}` })
  await expect.poll(async () => {
    const got = await alice.api.rpc<WatchGet>('watch.get', { watch_id: watchID })
    return got.notifications.map((n) => n.condition)
  }, { timeout: 60_000, message: 'the server records a needs-you notice with no page open' }).toContain('needs_you')

  await page.goto(`${alice.url}&page=settings&id=watches&watch=${watchID}`)
  const detail = page.getByRole('dialog')
  const notices = detail.getByRole('region', { name: 'Notifications' })
  await expect(notices.getByRole('listitem').first()).toBeVisible()
  await expect(notices.getByRole('list', { name: 'Delivery' }).first()).toContainText('In-app')
  await expect(detail.getByRole('region', { name: 'What the server sees' })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('watch-detail.png') })
  await testInfo.attach('watch detail', { body: await page.screenshot(), contentType: 'image/png' })

  await page.keyboard.press('Escape')
  await expect(detail).toBeHidden()
  const list = page.getByRole('list', { name: 'Active watches' })
  await expect(list.getByRole('listitem')).toHaveCount(1)
  await expect(list).toContainText('Last notice:')
  expect(errors).toEqual([])
})
