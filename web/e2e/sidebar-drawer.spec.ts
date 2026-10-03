// The narrow-window sidebar drawer and the keyboard. The drawer is a modal
// dialog, so it stands the shell's global keys down while it is open - the
// same contract every other dialog has. Mod+B is the exception it answers
// itself, because it is the key that opened it.

import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

test('the drawer answers the key that opened it and gives the rest back', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  // Narrow enough for the drawer, wide enough to be a desktop window with a
  // real keyboard attached.
  await page.setViewportSize({ width: 600, height: 800 })
  await page.goto(alice.url)

  const drawer = page.getByRole('dialog', { name: 'Runs' })
  const palette = page.locator('[data-slot="command-input"]')
  const opener = page.getByRole('banner', { name: 'Aether' }).getByRole('button', { name: 'Expand sidebar' })
  await expect(opener).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Surfaces' })).toBeHidden()
  await opener.focus()

  await page.keyboard.press('Control+b')
  await expect(drawer).toBeVisible()
  await expect(drawer.getByRole('navigation', { name: 'Surfaces' })).toBeVisible()
  await page.keyboard.press('Control+b')
  await expect(drawer).toBeHidden()
  await expect(opener).toBeFocused()

  await page.keyboard.press('Control+b')
  await expect(drawer).toBeVisible()
  // Every other shell key belongs to the drawer while it is open.
  await page.keyboard.press('Control+k')
  await expect(palette).toHaveCount(0)

  await page.keyboard.press('Escape')
  await expect(drawer).toBeHidden()
  await expect(opener).toBeFocused()
  await page.keyboard.press('Control+k')
  await expect(palette).toHaveCount(1)
})
