// The run list on a phone. Opening a run from it has to leave that run on
// screen: before the drawer existed, the 280px pane stayed over the terminal
// it had just opened and only a 26px Collapse button took it away.

import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('the phone drawer closes onto the run it opened', async ({ page, aether }) => {
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

  await page.getByRole('button', { name: 'Expand sidebar' }).tap()
  const drawer = page.getByRole('dialog', { name: 'Runs' })
  await expect(drawer).toBeVisible()

  const row = drawer.getByRole('button', { name: /read the deployment log/ })
  await expect(row).toBeVisible()
  // A finger needs about 44px where the desktop row is 28; the coarse-pointer
  // scale is what a phone is actually hitting here.
  const box = await row.boundingBox()
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(40)

  await row.tap()

  await expect(drawer).toBeHidden()
  await expect(
    page.getByRole('heading', { name: 'read the deployment log', exact: true }),
  ).toBeVisible()

  // The status bar is the tightest row in the shell, and on a coarse pointer
  // its controls are finger-sized rather than the desktop's 22px.
  const theme = page.getByRole('button', { name: /^Theme:/ })
  await expect(theme).toBeVisible()
  expect((await theme.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(40)

  // Nothing the drawer left behind is over the run: the shell still fits the
  // phone in both directions.
  const overflow = await page.evaluate(() =>
    Math.max(
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
      document.documentElement.scrollHeight - document.documentElement.clientHeight,
    ),
  )
  expect(overflow).toBe(0)
})
