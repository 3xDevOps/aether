import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

async function openFromPalette(page: Page, label: string) {
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Command Palette' })
  await dialog.getByRole('combobox').fill(label)
  await dialog.getByRole('option', { name: label, exact: true }).click()
}

test('keyboard focus survives the members tabs, the invite dialog and the workspace row menu', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  await page.goto(alice.url)

  await openFromPalette(page, 'Members')
  const devices = page.getByRole('tab', { name: 'Devices' })
  await devices.focus()
  await page.keyboard.press('Space')
  await expect(page.getByRole('form', { name: 'Approve a device' })).toBeVisible()
  await expect(devices).toBeFocused()
  await page.getByRole('tab', { name: 'Members' }).click()
  await expect(page.getByRole('tab', { name: 'Members' })).toBeFocused()

  const invite = page.getByRole('button', { name: 'Invite…' })
  await invite.click()
  await expect(page.getByRole('dialog', { name: 'Invite a member' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(invite).toBeFocused()

  await openFromPalette(page, 'Manage workspaces')
  await page.getByRole('button', { name: 'Open project' }).focus()
  await page.keyboard.press('Tab')
  const more = page.getByRole('button', { name: 'More actions for project' })
  await expect(more).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('menu')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(more).toBeFocused()

  await page.keyboard.press('Enter')
  await page.getByRole('menuitem', { name: 'Delete…' }).click()
  await expect(page.getByRole('alertdialog', { name: 'Delete project?' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(more).toBeFocused()
})
