// A form on a phone with the soft keyboard up. `interactive-widget=
// resizes-content` shrinks the layout viewport when the keyboard opens, and a
// short viewport is that state - Chromium's device emulation resolves `dvh`
// against the height the page loaded at, so the phone here starts short
// rather than being resized mid-test.
//
// A centred dialog on that viewport puts its footer under the keyboard.
// Narrow viewports anchor it to the top instead, so it shortens from the
// bottom and its own scroll reaches the footer.

import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test('the launch form keeps its footer on screen with the keyboard up', async ({
  page,
  aether,
}) => {
  // Roughly what a phone keyboard leaves of an 844px screen.
  const height = 440
  await page.setViewportSize({ width: 390, height })

  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  await page.goto(alice.url)

  await page.getByRole('button', { name: 'Expand sidebar' }).tap()
  await page
    .getByRole('dialog', { name: 'Runs' })
    .getByRole('button', { name: 'New run' })
    .tap()

  const dialog = page.getByRole('dialog', { name: 'Launch a run' })
  await expect(dialog).toBeVisible()

  // Polled: the dialog settles into place through its open animation.
  await expect
    .poll(async () => (await dialog.boundingBox())?.y ?? -1)
    .toBeLessThanOrEqual(16)
  const box = await dialog.boundingBox()
  if (!box) throw new Error('the launch form did not render')
  expect(box.y + box.height).toBeLessThanOrEqual(height)
  await expect(dialog.getByRole('button', { name: 'Launch' })).toBeInViewport({
    ratio: 1,
  })
})
