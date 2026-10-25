// A server with no edge cannot mint a connector code; Settings shows the
// server's own error, as aether mcp prints it.

import { expect, test } from './fixtures'

test('Settings shows why this server cannot connect ChatGPT or Claude', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'Settings', exact: true }).click()
  const section = page.getByRole('region', { name: 'ChatGPT and Claude' })
  await section.getByRole('button', { name: 'Get a code' }).click()
  await expect(section.getByRole('alert')).toContainText('this server has no edge (edge-url is empty)')
})
