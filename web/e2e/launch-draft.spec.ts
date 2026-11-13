import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

test('outside clicks and Escape collapse the new session draft, which resumes after reload', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.emulateMedia({ colorScheme: 'dark' })
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))

  await page.goto(alice.url)
  const nav = page.getByRole('navigation', { name: 'Aether' })
  await nav.getByRole('button', { name: 'New session' }).click()
  const dialog = page.getByRole('dialog', { name: 'New session' })
  await dialog.getByLabel('Task').fill('keep this draft')
  await nav.getByRole('button', { name: 'Search' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByRole('dialog', { name: 'Command Palette' })).toBeVisible()
  await page.keyboard.press('Escape')
  const resume = page.getByRole('button', { name: 'Resume new session draft' })
  await expect(resume).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('collapsed-new-session.png'), fullPage: true })

  await resume.click()
  await expect(dialog.getByLabel('Task')).toHaveValue('keep this draft')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(resume).toBeVisible()

  await page.reload()
  await expect(page.getByRole('button', { name: 'Resume new session draft' })).toBeVisible()
  await page.getByRole('button', { name: 'Resume new session draft' }).click()
  const restored = page.getByRole('dialog', { name: 'New session' })
  await expect(restored.getByLabel('Task')).toHaveValue('keep this draft')

  await restored.getByRole('button', { name: 'Close' }).click()
  await expect(page.getByRole('button', { name: 'Resume new session draft' })).toHaveCount(0)
  await nav.getByRole('button', { name: 'New session' }).click()
  await expect(page.getByRole('dialog', { name: 'New session' }).getByLabel('Task')).toHaveValue('')
})
