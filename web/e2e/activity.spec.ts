// Activity reads the real workspace log and the real agent-message history,
// and its one Filter popover is a Radix popover holding Radix selects, which
// only a real browser stacks and dismisses the way a person meets them.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

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
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'Activity', exact: true }).click()
  const feed = page.getByRole('region', { name: 'Activity feed' })
  await expect(feed.getByRole('listitem').filter({ hasText: 'Run status' }).first()).toBeVisible({ timeout: 120_000 })

  await page.getByRole('button', { name: 'Filter', exact: true }).click()
  const filter = page.getByRole('dialog')
  await filter.getByLabel('Show').click()
  await page.getByRole('option', { name: 'Run status' }).click()
  await expect(page.getByRole('button', { name: 'Filter · 1' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(filter).toBeHidden()
  const rows = feed.getByRole('listitem')
  await expect(rows.first()).toContainText('Run status')
  for (const text of await rows.allTextContents()) expect(text).toContain('Run status')

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
