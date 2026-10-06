// A template row launches first and schedules from its menu; the schedule
// preview is whatever the real server's cron parser answered.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'launching the template needs a reachable Docker daemon')

test('a template schedules from its row menu and launches from its row', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const task = 'run the nightly sweep'
  await alice.api.rpc('template.save', {
    workspace_id: workspaces[0].id,
    name: 'nightly',
    task,
    harness: 'fake',
    mode: 'headless',
  })

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'Templates', exact: true }).click()
  const row = page.getByRole('listitem', { name: 'Template nightly' })
  await expect(row).toContainText(`Background · ${task}`)
  await expect(page.getByRole('form')).toHaveCount(0)

  await row.getByRole('button', { name: 'More for nightly' }).click()
  await page.getByRole('menuitem', { name: 'Schedule…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Schedule nightly' })
  await dialog.getByLabel('Schedule (UTC)').fill('0 6 * * *')
  await dialog.getByRole('button', { name: 'Schedule', exact: true }).click()
  await expect(dialog.getByText(/^Next launch/)).toContainText('06:00:00 GMT')
  await page.keyboard.press('Escape')
  await expect(row).toContainText('Scheduled')
  const { schedules } = await alice.api.rpc<{ schedules: { template: string; cron: string }[] }>('schedule.list', {
    workspace_id: workspaces[0].id,
  })
  expect(schedules).toEqual([expect.objectContaining({ template: 'nightly', cron: '0 6 * * *' })])

  await row.getByRole('button', { name: 'Launch nightly' }).click()
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible({ timeout: 60_000 })
})
