// The Release channel section against a real gateway and server. The real
// server answers server.update_status, so the channel on screen is the one
// the wire carries; only server.release_channel_set is answered here, since
// the harness keeps the server off GitHub's channel pointers, which a real
// answer needs, and saving would change the server under test.

import type { Page, Route } from '@playwright/test'
import type { ServerReleaseChannelSet } from '../src/lib/types'
import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

const json = (route: Route, body: unknown) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })

const answer = (channel: 'stable' | 'beta' | 'nightly', over: Partial<ServerReleaseChannelSet>): ServerReleaseChannelSet => ({
  channel,
  previous: 'beta',
  saved: false,
  running: { version: 'v0.9.0-nightly.12', build: 412 },
  target: { version: 'v0.9.0', build: 380 },
  wait: true,
  ...over,
})

async function routeChannelSet(page: Page) {
  const calls: { channel: string; dry_run?: boolean }[] = []
  await page.route('**/api/v1/server.release_channel_set', (route) => {
    const call = route.request().postDataJSON() as { channel: 'stable' | 'beta' | 'nightly'; dry_run?: boolean }
    calls.push(call)
    return json(route, answer(call.channel, { saved: !call.dry_run }))
  })
  return calls
}

test('a super admin confirms nightly and a switch that makes the server wait before anything saves', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  const calls = await routeChannelSet(page)

  await page.goto(`${alice.url}&page=settings&id=server`)
  const section = page.getByRole('region', { name: 'Release channel' })
  // A server that has not chosen a channel follows beta.
  await expect(section.getByRole('radio', { name: 'Beta' })).toBeChecked()

  await section.getByRole('radio', { name: 'Nightly' }).click()
  const nightly = page.getByRole('dialog', { name: 'Switch to nightly builds?' })
  await expect(nightly.getByText('Not recommended unless you are developing Cloudfield.')).toBeVisible()
  await nightly.getByRole('button', { name: 'Cancel' }).click()
  expect(calls).toEqual([])

  await section.getByRole('radio', { name: 'Stable' }).click()
  const wait = page.getByRole('dialog', { name: 'Switch to stable?' })
  await expect(wait).toContainText(
    'This server runs v0.9.0-nightly.12 (build 412). Stable is at v0.9.0 (build 380). The server stays on its current build until stable reaches build 412, then updates as usual. It never installs an older build.',
  )
  expect(calls).toEqual([{ channel: 'stable', dry_run: true }])
  await wait.getByRole('button', { name: 'Switch to stable' }).click()
  await expect.poll(() => calls.length).toBe(2)
  expect(calls[1]).toEqual({ channel: 'stable' })
})

test('a member who is not a super admin sees the channel and no way to change it', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  const invite = await aether.invite(alice)
  const bob = await aether.member('bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite, name: 'bob' })

  await page.goto(`${bob.url}&page=settings&id=server`)
  const section = page.getByRole('region', { name: 'Release channel' })
  await expect(section.getByText('Beta', { exact: true })).toBeVisible()
  await expect(section.getByRole('radio')).toHaveCount(0)
})
