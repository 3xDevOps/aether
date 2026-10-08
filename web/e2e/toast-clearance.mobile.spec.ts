// A toast on a phone has to stay clear of the bottom edge and the home
// indicator. Sonner keeps two offsets and swaps to `mobileOffset` below
// 600px, so the app's own offset has to be given to both.

import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test('a toast clears the bottom edge on a phone', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  // Deleting a template is the cheapest real action that ends in a toast.
  await alice.api.rpc('template.save', {
    workspace_id: workspaces[0].id,
    name: 'nightly',
    task: 'run the nightly sweep',
    harness: 'fake',
  })

  await page.goto(alice.url)
  await page.getByRole('button', { name: 'More navigation' }).tap()
  await page.getByRole('menuitem', { name: 'Templates', exact: true }).tap()
  await page.getByRole('button', { name: 'More for nightly', exact: true }).tap()
  await page.getByRole('menuitem', { name: 'Delete', exact: true }).tap()
  await page
    .getByRole('alertdialog')
    .getByRole('button', { name: 'Delete', exact: true })
    .tap()

  const toast = page.locator('[data-sonner-toast]').first()
  await expect(toast).toBeVisible()

  // Polled: the toast slides up into place, so its first box is above where
  // it settles. What must hold is where it comes to rest: 8px clear of the
  // bottom edge, which on a phone without a home indicator is the screen's.
  const height = page.viewportSize()!.height
  await expect
    .poll(async () => {
      const box = await toast.boundingBox()
      if (!box) return Number.POSITIVE_INFINITY
      return Math.round(box.y + box.height - (height - 8))
    })
    .toBeLessThanOrEqual(0)
})
