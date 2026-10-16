// Insights counts what the real server recorded when a real run ended, so
// the finished run here is one the fake harness actually ran to completion.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the run Insights counts needs a reachable Docker daemon')

test('Insights counts a finished run and keeps it across period and scope', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const task = 'write the insights fixture'
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    mode: 'headless',
    task,
  })
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { status: string } }>('run.get', { run_id: run.id })).run.status, {
      timeout: 180_000,
      intervals: [250, 500, 1_000, 2_000],
    })
    .toBe('completed')

  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Insights', exact: true }).click()

  const counted = async (period: string) => {
    await expect(page.getByText(`${period}: 1 run finished.`, { exact: true })).toBeVisible({ timeout: 30_000 })
    await expect(page.getByRole('article', { name: 'Runs finished' }).getByText('1', { exact: true })).toBeVisible()
    await expect(page.getByRole('article', { name: 'Success rate' })).toContainText('100%')
    const work = page.getByRole('region', { name: 'Finished work' })
    await expect(work.getByRole('listitem')).toHaveCount(1)
    await expect(work.getByRole('listitem')).toContainText(task)
    await expect(work.getByRole('listitem')).toContainText('Completed')
    await expect(page.getByRole('main').getByRole('alert')).toHaveCount(0)
  }
  await counted('Last 7 days')

  await page.getByRole('combobox', { name: 'Period' }).click()
  await page.getByRole('option', { name: 'Last 12 months' }).click()
  await counted('Last 12 months')

  const everyone = page.getByRole('group', { name: 'Whose runs' }).getByRole('button', { name: 'Everyone' })
  await everyone.click()
  await expect(everyone).toHaveAttribute('aria-pressed', 'true')
  await counted('Last 12 months')

  await page.getByRole('combobox', { name: 'Period' }).click()
  await page.getByRole('option', { name: 'Last 30 days' }).click()
  await counted('Last 30 days')
  expect(errors).toEqual([])
})
