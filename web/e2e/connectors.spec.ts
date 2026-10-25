// A server with no edge cannot mint a connector code; Settings, reached from
// the palette, says so before a code and then shows the server's own error,
// as aether mcp prints it.

import { expect, test } from './fixtures'

test('Settings shows why this server cannot connect ChatGPT or Claude', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  await page.goto(alice.url)
  await expect(page.getByRole('navigation', { name: 'Aether' })).toBeVisible()
  await page.keyboard.press('ControlOrMeta+k')
  await page.getByRole('option', { name: 'Connect ChatGPT or Claude' }).click()
  const section = page.locator('#connectors')
  await expect(section).toBeInViewport()
  await expect(section.getByText('No connectors yet')).toBeVisible()
  await expect(section.getByText('This server has no edge')).toBeVisible()
  await section.getByRole('tab', { name: 'Claude' }).click()
  await expect(section.getByRole('link', { name: 'Claude connectors' })).toBeVisible()
  await section.getByRole('button', { name: 'Get a code' }).click()
  const alert = section.getByRole('alert')
  await expect(alert).toContainText('this server has no edge (edge-url is empty)')
  await expect(alert.getByRole('link', { name: 'Edge guide' })).toBeVisible()
})
