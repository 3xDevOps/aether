import { expect, test } from './mobile'

test('a phone moves between the settings list and one category, and search opens a setting', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const url = new URL(alice.url)
  url.searchParams.set('page', 'settings')
  await page.goto(url.toString())

  const nav = page.getByRole('navigation', { name: 'Settings' })
  await nav.getByRole('button', { name: 'Notifications', exact: true }).click()
  await expect(page.getByRole('heading', { level: 2, name: 'Notifications' })).toBeVisible()
  await expect(nav).toBeHidden()
  await expect(page).toHaveURL(/[?&]id=notifications\b/)

  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await expect(nav.getByRole('button', { name: 'Notifications', exact: true })).toBeFocused()
  expect(new URL(page.url()).searchParams.get('id')).toBeNull()

  await page.goBack()
  await expect(page.getByRole('heading', { level: 2, name: 'Notifications' })).toBeVisible()
  await page.goBack()
  await expect(nav.getByRole('button', { name: 'Notifications', exact: true })).toBeFocused()

  await nav.getByRole('searchbox', { name: 'Search settings' }).fill('slack')
  await expect(nav.getByRole('status')).toHaveText('1 setting')
  await nav.getByRole('button', { name: /^Slack/ }).click()
  await expect(page).toHaveURL(/[?&]id=slack\b/)
  await expect(page.getByRole('heading', { level: 2, name: 'Notifications' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Slack' })).toBeInViewport()

  await page.getByRole('heading', { level: 2, name: 'Notifications' }).click()
  await page.keyboard.press('/')
  await expect(nav.getByRole('searchbox', { name: 'Search settings' })).toBeFocused()
})
