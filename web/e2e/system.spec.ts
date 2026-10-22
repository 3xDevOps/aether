// Insights › System reads the real server's /proc counters and a real run
// container's Docker stats, so only a live server with a Docker daemon can
// show a measured run in the list.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the run container System measures needs a reachable Docker daemon')

test('Insights › System measures a running run and opens it from the list', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const task = 'hold the system fixture'
  // An interactive run keeps its supervised shell after the fake agent exits, so its container stays up.
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    mode: 'tui',
    task,
  })
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { status: string } }>('run.get', { run_id: run.id })).run.status, {
      timeout: 180_000,
      intervals: [250, 500, 1_000, 2_000],
    })
    .toBe('running')

  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Insights', exact: true }).click()
  await page.getByRole('tab', { name: 'System', exact: true }).click()

  await expect(page.getByRole('group', { name: 'CPU', exact: true })).toContainText(/\d%/, { timeout: 30_000 })
  await expect(page.getByRole('group', { name: 'Memory', exact: true })).toContainText(/ of /)
  const row = page.getByRole('list', { name: 'Containers by usage' }).getByRole('button', { name: new RegExp(task) })
  await expect(row).toContainText(/cores · .*% of host/, { timeout: 30_000 })

  await row.click()
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  expect(errors).toEqual([])
})
