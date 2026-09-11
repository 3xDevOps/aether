// Acting on a run and reading its diff from a phone. The run header's verbs
// are the main way to steer a run from the Terminal, Diff, Events and
// Overview tabs, and on a coarse pointer they are reachable only through the
// one Actions menu - so a menu that did not open, or opened items too small
// to hit, would leave a phone with no verbs at all.

import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a phone steers a run from the Actions menu and reads its diff', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  await alice.api.rpc('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task: 'write the result file',
  })

  await page.goto(alice.url)

  await page.getByRole('button', { name: 'Expand sidebar' }).tap()
  await page
    .getByRole('dialog', { name: 'Runs' })
    .getByRole('button', { name: /write the result file/ })
    .tap()
  await expect(
    page.getByRole('heading', { name: 'write the result file', exact: true }),
  ).toBeVisible()

  // Every verb is behind this one button, and the header stays one row.
  const actions = page.getByRole('button', { name: 'Actions', exact: true })
  await expect(actions).toBeVisible()
  expect((await actions.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(40)
  await actions.tap()

  const menu = page.getByRole('menu')
  const protect = menu.getByRole('menuitem', { name: 'Protect run' })
  await expect(protect).toBeVisible()
  expect((await protect.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(40)
  await protect.tap()

  await expect(page.getByRole('img', { name: /^Protected:/ })).toBeVisible()
  await actions.tap()
  await expect(menu.getByRole('menuitem', { name: 'Unprotect run' })).toBeVisible()
  await page.keyboard.press('Escape')

  // The agent writes result.txt a second after it greets, and the tab fetches
  // the patch once per revision, so Refresh is what asks again.
  const pane = page.locator('.xterm-rows')
  await expect(pane).toContainText('agent-ready', { timeout: 3 * 60 * 1000 })

  await page.getByRole('tab', { name: 'Diff' }).tap()
  const refresh = page.getByRole('button', { name: 'Refresh', exact: true })
  await expect(async () => {
    await refresh.tap()
    await expect(page.getByText('result.txt')).toBeVisible({ timeout: 2000 })
  }).toPass({ timeout: 60 * 1000 })

  // Nothing stands between the header and the first line of the patch: with
  // no snapshots there is no interval list, and on a coarse pointer long
  // lines wrap rather than scrolling each file section sideways.
  await expect(page.getByText('result.txt')).toBeInViewport()
  await expect(page.getByText('+hello-from-agent')).toBeVisible()
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
  )
  expect(overflow).toBe(0)
})
