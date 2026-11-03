// What a phone does to the connection: it is pocketed, loses its network and
// comes back. The dashboard has to show the last state it knew at once, say
// plainly what failed, and reconnect without a reload when the phone returns.

import type { WebSocketRoute } from '@playwright/test'
import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

test('a returning phone paints the saved state, shows the real error and reconnects', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  let offline = false
  const sockets: WebSocketRoute[] = []
  await page.routeWebSocket(/\/ws\/events/, (ws) => {
    if (offline) {
      void ws.close({ code: 1011 })
      return
    }
    sockets.push(ws)
    ws.connectToServer()
  })
  const refusal = 'server unreachable: dial tcp 100.64.0.9:22: i/o timeout'
  await page.route(/\/api\/(?!local)/, (route) =>
    offline
      ? route.fulfill({ status: 503, json: { error: { code: -32004, message: refusal } } })
      : route.continue(),
  )

  await page.goto(alice.url)
  const footer = page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: /, (Connecting|Live|Reconnecting|Offline)$/ })
  await expect(page.getByText('project').first()).toBeVisible()
  await expect(page.getByText(/Saved state from/)).toHaveCount(0)

  // Back from the pocket: the page reloads with no network at all.
  offline = true
  await page.reload()
  await expect(page.getByText(/^Saved state from /)).toBeVisible()
  await expect(page.getByText('project').first()).toBeVisible()
  await expect(page.getByRole('heading', { name: /offline|unreachable/i })).toHaveCount(0)
  await expect(page.getByText(refusal).first()).toBeVisible()

  offline = false
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByText(/Saved state from/)).toHaveCount(0, { timeout: 5_000 })
  await expect(footer).toHaveAccessibleName(/, Live$/)
  expect(sockets.length).toBeGreaterThan(0)
})
