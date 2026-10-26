// A template is picked from the library list, schedules from its menu and
// launches with a value typed over a parameter default; the schedule preview
// is whatever the real server's cron parser answered.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'launching the template needs a reachable Docker daemon')

test('a template schedules from its menu and launches with a parameter filled in', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  await alice.api.rpc('template.save', {
    workspace_id: workspaces[0].id,
    name: 'nightly',
    task: 'run the {{scope}} sweep on {{branch}}',
    harness: 'fake',
    mode: 'headless',
    params: { scope: 'nightly', branch: 'main' },
  })

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Templates', exact: true }).click()
  await expect(page.getByRole('option', { name: 'Template nightly' })).toContainText('Background')
  const detail = page.getByRole('region', { name: 'nightly' })
  await expect(detail).toContainText('2 of 2 filled')
  await expect(page.getByRole('form')).toHaveCount(0)

  await detail.getByRole('button', { name: 'More for nightly' }).click()
  await page.getByRole('menuitem', { name: 'Schedule…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Schedule nightly' })
  await dialog.getByLabel('Schedule (UTC)').fill('0 6 * * *')
  await dialog.getByRole('button', { name: 'Schedule', exact: true }).click()
  await expect(dialog.getByText(/^Next launch/)).toContainText('06:00:00 GMT')
  await page.keyboard.press('Escape')
  await expect(detail).toContainText('Scheduled 0 6 * * * UTC')
  const { schedules } = await alice.api.rpc<{ schedules: { template: string; cron: string }[] }>('schedule.list', {
    workspace_id: workspaces[0].id,
  })
  expect(schedules).toEqual([expect.objectContaining({ template: 'nightly', cron: '0 6 * * *' })])

  await detail.getByRole('textbox', { name: /^scope/ }).fill('weekly')
  await expect(detail.getByRole('region', { name: 'Preview' })).toHaveText('run the weekly sweep on main')
  await detail.getByRole('button', { name: 'Launch nightly' }).click()
  await expect(page.getByRole('heading', { name: 'run the weekly sweep on main', exact: true })).toBeVisible({ timeout: 60_000 })
})
