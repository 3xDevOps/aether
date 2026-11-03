// The run list on a phone, under the Sessions tab. Opening a run from it has
// to leave that run on screen with finger-sized controls.

import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

test('the Sessions tab opens a run full screen', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  await alice.api.rpc('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task: 'read the deployment log',
  })

  await page.goto(alice.url)

  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Sessions' }).tap()
  const row = page.getByRole('main').getByRole('region', { name: 'Sessions' }).getByRole('button', { name: /read the deployment log/ })
  await expect(row).toBeVisible()
  // A finger needs about 44px where the desktop row is 28; the coarse-pointer
  // scale is what a phone is actually hitting here.
  const box = await row.boundingBox()
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(40)

  await row.tap()

  await expect(
    page.getByRole('heading', { name: 'read the deployment log', exact: true }),
  ).toBeVisible()

  // Focus lands on the run. The top bar says Session, so
  // the title shows once, in the run header, and its controls are finger-sized.
  await expect(page.getByRole('heading', { level: 1 })).toBeFocused()
  const bar = page.getByRole('banner')
  await expect(bar).toContainText('Session')
  await expect(bar).not.toContainText('read the deployment log')
  const search = bar.getByRole('button', { name: 'Search', exact: true })
  expect((await search.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(40)

  // The shell still fits the phone in both directions.
  const overflow = await page.evaluate(() =>
    Math.max(
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
      document.documentElement.scrollHeight - document.documentElement.clientHeight,
    ),
  )
  expect(overflow).toBe(0)
})
