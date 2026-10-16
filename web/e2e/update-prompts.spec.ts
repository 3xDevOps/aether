// The update experience against a real gateway and server. Only the update
// wire is answered here: a real release lookup needs GitHub, and a real
// install would replace the binary under test. Everything around it - the
// shell, the store, the timers, the gate a refused call raises - is the
// shipped dashboard.

import type { Page, Route } from '@playwright/test'
import type { ServerUpdateStatus, UpdateApplyResult, UpdateStatus } from '../src/lib/types'
import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

const checkedAt = '2026-10-16T12:00:00Z'

function clientStatus(latest: string, cli: Partial<UpdateStatus['cli']> = {}, extra: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    cli: {
      version: 'v0.7.0',
      commit: 'abc1234',
      latest,
      update_available: latest !== 'v0.7.0',
      release_url: `https://github.com/3xDevOps/Aether/releases/tag/${latest}`,
      dev: false,
      disabled: false,
      can_self_update: true,
      checked_at: checkedAt,
      ...cli,
    },
    server_version: 'v0.7.0',
    server_behind: false,
    supervised: false,
    install_method: 'direct',
    cli_path: '/x/aether',
    client_update_required: false,
    can_self_update: true,
    ...extra,
  }
}

const serverCurrent: ServerUpdateStatus = {
  server_version: 'v0.7.0',
  latest: 'v0.7.0',
  latest_version: 'v0.7.0',
  update_available: false,
  capable: true,
  checked_at: checkedAt,
}

const serverBehind: ServerUpdateStatus = {
  server_version: 'v0.7.0',
  latest: 'v0.8.0',
  latest_version: 'v0.8.0',
  update_available: true,
  capable: true,
  notes_url: 'https://github.com/3xDevOps/Aether/releases/tag/v0.8.0',
}

const json = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

/** Answers update.check with whatever `answer` returns at the time of the call. */
async function routeUpdateCheck(page: Page, answer: () => UpdateStatus) {
  const bodies: unknown[] = []
  await page.route('**/local/v1/update.check', (route) => {
    bodies.push(route.request().postDataJSON())
    return json(route, answer())
  })
  return bodies
}

async function routeServerStatus(page: Page, status: ServerUpdateStatus) {
  await page.route('**/api/v1/server.update_status', (route) => json(route, status))
}

const shell = (page: Page) => page.getByRole('navigation', { name: 'Aether' })
const footer = (page: Page) => shell(page).getByRole('button', { name: /, (Connecting|Live|Reconnecting|Offline)$/ })
const updates = (page: Page) => page.getByRole('dialog', { name: 'Updates' })

test('a release found by the half-hourly check surfaces in the sidebar', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))

  let released = false
  const checks = await routeUpdateCheck(page, () => clientStatus(released ? 'v0.8.0' : 'v0.7.0'))
  await routeServerStatus(page, serverCurrent)

  await page.clock.install()
  await page.goto(alice.url)
  await expect(footer(page)).toHaveAccessibleName(/, Live$/)
  await expect.poll(() => checks.length).toBeGreaterThan(0)
  const notice = shell(page).getByRole('status').filter({ hasText: /is available/ })
  await expect(notice).toHaveCount(0)

  released = true
  // RECHECK_MS is 30 minutes, jittered by up to 10% either way.
  await page.clock.fastForward('34:00')

  await expect(notice).toHaveText(/^Aether 0\.8\.0 is available\s*· Update$/)
  await notice.getByRole('button', { name: 'Update' }).click()
  await expect(updates(page).getByText('Aether v0.8.0 is available.')).toBeVisible()
})

test('Check for updates says up to date, then shows a failed lookup verbatim', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))

  const rateLimited = 'check for releases: api.github.com: 403 rate limit exceeded'
  let failing = false
  const checks = await routeUpdateCheck(page, () =>
    failing
      ? clientStatus('', { latest: undefined, update_available: false, checked_at: '' }, { cli_error: rateLimited })
      : clientStatus('v0.7.0'),
  )
  await routeServerStatus(page, serverCurrent)

  await page.goto(alice.url)
  await footer(page).click()
  await page.getByRole('menuitem', { name: 'Check for updates…' }).click()

  const dialog = updates(page)
  await expect(dialog.getByRole('status').first()).toHaveText(
    /^You're up to date \(v0\.7\.0\)\. Checked \d{1,2}:\d{2}( [AP]M)?\.$/,
  )
  await expect(dialog.getByText('Aether on this computer is up to date (v0.7.0).')).toBeVisible()

  failing = true
  const before = checks.length
  await dialog.getByRole('button', { name: 'Check now' }).click()

  await expect(dialog.getByText('Could not check for a new Aether release.')).toBeVisible()
  await expect(dialog.getByText(rateLimited, { exact: true })).toBeVisible()
  await expect(dialog.getByText(/You're up to date/)).toHaveCount(0)
  expect(checks.slice(before)).toContainEqual({ refresh: true })
})

test('a server that refuses this client gates the dashboard until it updates', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))

  // Flipped once update.apply answers: from then on this client runs the
  // server's release, so the server accepts it and update.check says so.
  let upgraded = false
  const refusal = 'this aether is v0.7.0 and the server runs v0.8.0; update this client first: run `aether update`, then retry'
  const refuse = (route: Route) => {
    const method = new URL(route.request().url()).pathname.split('/').pop()
    if (upgraded || method === 'server.info' || method === 'capabilities' || method === 'server.update_status') {
      return route.fallback()
    }
    return json(route, {
      error: {
        code: -32005,
        message: refusal,
        data: { client_version: 'v0.7.0', server_version: 'v0.8.0', min_client_version: 'v0.8.0', command: 'curl https://example.invalid/x | sh' },
      },
    }, 426)
  }
  await page.route('**/api/v1/**', refuse)
  const checks = await routeUpdateCheck(page, () =>
    upgraded
      ? clientStatus('v0.8.0', { version: 'v0.8.0' }, { target_version: 'v0.8.0' })
      : clientStatus('v0.8.0', {}, { client_update_required: true, min_client_version: 'v0.8.0', target_version: 'v0.8.0' }),
  )
  await routeServerStatus(page, serverCurrent)

  let release: () => void = () => {}
  const held = new Promise<void>((resolve) => { release = resolve })
  const installed: UpdateApplyResult = { updated: ['/x/aether'], version: 'v0.8.0', restarting: false, rebuilding: false }
  await page.route('**/local/v1/update.apply', async (route) => {
    await held
    upgraded = true
    await json(route, installed)
  })

  await page.clock.install()
  await page.goto(alice.url)

  const gate = page.getByRole('region', { name: 'Update Aether to keep using this server' })
  await expect(gate.getByRole('heading', { name: 'Update Aether to keep using this server' })).toBeFocused()
  await expect(gate).toContainText(`The server said: ${refusal}. It accepts clients from v0.8.0.`)
  await expect(gate.getByText('aether update', { exact: true })).toBeVisible()
  await expect(gate.getByText(/curl/)).toHaveCount(0)
  await expect(gate.getByRole('button', { name: 'Copy details' })).toBeVisible()
  await expect(gate.getByRole('button', { name: 'Reload' })).toBeVisible()

  await gate.getByRole('button', { name: 'Update now' }).click()
  await expect(gate.getByRole('status')).toHaveText('Downloading and verifying v0.8.0…')
  release()
  await expect(gate.getByText('Aether v0.8.0 is installed.')).toBeVisible()

  // The next check after a minute finds the client accepted, which ends
  // the gate with one reload.
  const before = checks.length
  await page.clock.fastForward('01:01')
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect.poll(() => checks.length).toBeGreaterThan(before)
  await expect(footer(page)).toHaveAccessibleName(/, Live$/)
  await expect(page.getByRole('heading', { name: 'Update Aether to keep using this server' })).toHaveCount(0)
})

test('the server updates before its clients, and only an admin can start it', async ({ page, browser, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  const bob = await aether.member('bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite: await aether.invite(alice), name: 'bob' })

  const open = async (target: Page, url: string, serverFirst: boolean) => {
    await routeUpdateCheck(target, () => clientStatus('v0.8.0', {}, { server_behind: true, server_first: serverFirst }))
    await routeServerStatus(target, serverBehind)
    await target.goto(url)
  }

  await open(page, alice.url, true)
  const adminNotice = shell(page).getByRole('status').filter({ hasText: /is available/ })
  await expect(adminNotice).toHaveText(/^Server 0\.8\.0 is available\s*· Update$/)
  await adminNotice.getByRole('button', { name: 'Update' }).click()

  const dialog = updates(page)
  const serverRow = dialog.getByRole('status').filter({ hasText: 'Server v0.8.0 is available.' })
  const clientRow = dialog.getByRole('status').filter({ hasText: 'Aether v0.8.0 is available.' })
  await expect(serverRow.getByRole('button', { name: 'Update now' })).toBeVisible()
  await expect(serverRow.getByRole('button', { name: 'Update when idle' })).toBeVisible()
  await expect(clientRow).toContainText('Update the server first, from its row above or with aether server update; this client updates after it.')
  await expect(clientRow.getByRole('button', { name: 'Update now' })).toHaveCount(0)
  const serverFirst = await serverRow.evaluate(
    (server, client) => (server.compareDocumentPosition(client as Node) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0,
    await clientRow.elementHandle(),
  )
  expect(serverFirst).toBe(true)

  const bobContext = await browser.newContext()
  try {
    const bobPage = await bobContext.newPage()
    await open(bobPage, bob.url, false)
    const memberNotice = shell(bobPage).getByRole('status').filter({ hasText: /is available/ })
    await expect(memberNotice).toHaveText(/^Server 0\.8\.0 is available, ask an admin\s*· Update$/)
    await memberNotice.getByRole('button', { name: 'Update' }).click()

    const memberRow = updates(bobPage).getByRole('status').filter({ hasText: 'Server v0.8.0 is available.' })
    await expect(memberRow).toContainText('Ask an admin to run aether server update')
    await expect(memberRow.getByRole('button', { name: 'Update when idle' })).toHaveCount(0)
    await expect(memberRow.getByRole('button', { name: 'Update now' })).toHaveCount(0)
  } finally {
    await bobContext.close()
  }
})
