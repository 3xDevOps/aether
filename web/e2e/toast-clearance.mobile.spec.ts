// A toast on a phone has to stay off the status bar. Sonner keeps two
// offsets and swaps to `mobileOffset` below 600px, falling back to a 16px
// default that lands inside the bar, so the app's own offset has to be given
// to both - and the bar is 44px on a coarse pointer, twice what it is for a
// mouse, which is what makes the default land inside it.

import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

test('a toast clears the status bar on a phone', async ({ page, aether }) => {
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
  await page.getByRole('button', { name: 'Expand sidebar' }).tap()
  await page
    .getByRole('dialog', { name: 'Runs' })
    .getByRole('button', { name: 'Templates', exact: true })
    .tap()
  await page.getByRole('button', { name: 'Delete', exact: true }).tap()
  await page
    .getByRole('alertdialog')
    .getByRole('button', { name: 'Delete', exact: true })
    .tap()

  const toast = page.locator('[data-sonner-toast]').first()
  const footer = page.getByRole('contentinfo')
  await expect(toast).toBeVisible()

  // Polled: the toast slides up into place, so its first box is above where
  // it settles. What must hold is where it comes to rest.
  await expect
    .poll(async () => {
      const box = await toast.boundingBox()
      const bar = await footer.boundingBox()
      if (!box || !bar) return Number.POSITIVE_INFINITY
      return Math.round(box.y + box.height - bar.y)
    })
    .toBeLessThanOrEqual(0)
})
