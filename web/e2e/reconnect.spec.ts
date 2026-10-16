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
  const footer = page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: /, (Connecting|Live|Reconnecting|Offline)$/ })
  await expect(footer).toHaveAccessibleName(/, Live$/)

  pocketed = true
  await connected[0].close({ code: 1011 })
  // Four failed retries is where the client gives up and says so. The next
  // one is then at least four seconds out, and climbing.
  await expect(footer).toHaveAccessibleName(/, Offline$/)
  await expect(page.getByRole('main').getByText(/^Offline/)).toBeVisible()
  await expect(page.getByRole('status').filter({ hasText: 'Offline' })).toBeAttached()

  pocketed = false
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))

  await expect(footer).toHaveAccessibleName(/, Live$/, { timeout: 2_000 })
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

// The reported contradiction: a refusal on one subscribe, then a replay
// reconnect that goes live without re-fetching the snapshot. The refusal is
// the edge's real wire shape; the reconnect around it is the real gateway
// and server.
test('a replay reconnect that goes live clears the edge refusal before it', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  let refusing = false
  const subscribes: { replay: boolean; after_seq: number }[] = []
  const live: WebSocketRoute[] = []
  let events = 0
  await page.routeWebSocket(/\/ws\/events/, (ws) => {
    if (refusing) {
      ws.onMessage((frame) => {
        subscribes.push(JSON.parse(String(frame)))
        ws.send(JSON.stringify({
          ok: false,
          code: -32004,
          error: 'server unreachable: connect to server srv123: edge.example.test refused: server is not connected to the edge (HTTP 503)',
          data: { refusal: { kind: 'server-not-connected', refusal: 'server is not connected to the edge' } },
        }))
        void ws.close({ code: 1008 })
      })
      return
    }
    live.push(ws)
    const server = ws.connectToServer()
    server.onMessage((frame) => {
      if (String(frame).includes('"seq"')) events++
      ws.send(frame)
    })
  })

  await page.goto(alice.url)
  const footer = page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: /, (Connecting|Live|Reconnecting|Offline|Unreachable)$/ })
  await expect(footer).toHaveAccessibleName(/, Live$/)
  // An event gives the client a cursor, so its next subscribe replays.
  await alice.api.rpc('workspace.add', { name: 'second', base_branch: 'main', environment: {} })
  await expect.poll(() => events).toBeGreaterThan(0)

  refusing = true
  await live[0].close({ code: 1011 })
  const problem = page.getByRole('status').filter({ hasText: 'Server registration failed' })
  await expect(problem).toContainText('has no live registration for server')
  await expect(problem).toContainText('aether-server edge status')
  // The announcer is screen-reader only; the line on screen must say the same.
  const line = page.getByText(/^Server registration failed · /).filter({ visible: true })
  await expect(line.first()).toBeVisible()
  await expect(line.first()).toContainText('has no live registration for server')
  await expect(footer).toHaveAccessibleName(/, Unreachable$/)
  expect(subscribes[0]?.replay).toBe(true)

  refusing = false
  await expect(footer).toHaveAccessibleName(/, Live$/, { timeout: 10_000 })
  await expect(problem).toHaveCount(0)
  await expect(line).toHaveCount(0)
  await expect(page.getByText(/Live · /)).toHaveCount(0)
})
