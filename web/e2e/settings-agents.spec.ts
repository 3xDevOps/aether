import { expect, test } from './fixtures'

test('Settings lists each agent and a search for sign in leads to it', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const url = new URL(alice.url)
  url.searchParams.set('page', 'settings')
  await page.goto(url.toString())

  const nav = page.getByRole('navigation', { name: 'Settings' })
  await nav.getByRole('searchbox', { name: 'Search settings' }).fill('sign in')
  await nav.getByRole('button', { name: /^Agent sign-in/ }).click()
  await expect(page.getByRole('heading', { level: 2, name: 'Agents' })).toBeVisible()
  const list = page.getByRole('list', { name: 'Agents' })
  await expect(list.getByText('Not installed').first()).toBeVisible()
  if (process.env.AETHER_SCREENSHOT_DIR) {
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.screenshot({ path: `${process.env.AETHER_SCREENSHOT_DIR}/settings-agents-${page.viewportSize()?.width}.png` })
  }

  await page.getByRole('button', { name: 'Open Agents' }).click()
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
})
