// A phone suspends its tab, changes network, and comes back from the pocket.
// Both halves are real here: the events socket is dropped until the client
// gives up and calls itself offline, then the tab returns with the same
// `visibilitychange` the browser fires, and the socket has to reopen at once
// instead of sitting out the rest of a backoff that caps at 30 seconds.
//
// The second scenario is the other thing a phone hits first: the dashboard
// token lives in the tab's session storage, so a bookmarked address, a second
// tab, or a restarted `aether gui` leaves the page holding a token the
// gateway rejects. That has to read as an expired link, with the gateway's
// own words, rather than as a server nobody can reach.

import type { WebSocketRoute } from '@playwright/test'
import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

test('a backgrounded tab reopens its event socket the moment it returns', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  // The pocket: while it is on, every handshake fails the way a phone with
  // no usable network fails. Off, the socket reaches the real gateway.
  let pocketed = false
  const connected: WebSocketRoute[] = []
  await page.routeWebSocket(/\/ws\/events/, (ws) => {
    if (pocketed) {
      ws.close({ code: 1011 })
      return
    }
    connected.push(ws)
    ws.connectToServer()
  })

  await page.goto(alice.url)
  const bar = page.locator('footer')
  await expect(bar).toContainText('Live')

  pocketed = true
  await connected[0].close({ code: 1011 })
  // Four failed retries is where the client gives up and says so. The next
  // one is then at least four seconds out, and climbing.
  await expect(bar).toContainText('Offline')

  pocketed = false
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))

  await expect(bar).toContainText('Live', { timeout: 2_000 })
})

test('a rejected token reads as an expired link, not an unreachable server', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const url = new URL(alice.url)
  url.searchParams.set('token', 'not-the-token-this-gateway-minted')

  await page.goto(url.toString())

  await expect(
    page.getByRole('heading', { name: 'This dashboard link has expired' }),
  ).toBeVisible()
  // The gateway's own refusal, not a guess about the network.
  await expect(page.getByText(/a valid gateway token is required/)).toBeVisible()
  await expect(page.getByText(/check your connection/i)).toHaveCount(0)
})
