// A watch is the server's, not the page's: with every dashboard tab closed it
// records one notice per need, says honestly where the notice went, never
// repeats a need that was answered, carries on across a server crash
// without a duplicate, holds pushes through quiet hours, and ends with its
// owner's membership.

import { createECDH, randomBytes } from 'node:crypto'
import path from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import type { Page } from '@playwright/test'

import { expect, test, type Member } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

interface Delivery { channel: string; status: string; detail?: string }
interface Notice { id: string; condition: string; title: string; body: string; deliveries: Delivery[] }
interface WatchGet {
  watch: { id: string; state: string; observed: { need?: string } }
  notifications: Notice[]
}

async function launch(alice: Member, task: string): Promise<string> {
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'mock',
    mode: 'acp',
    task,
  })
  return run.id
}

const needs = async (member: Member, watchID: string) =>
  (await member.api.rpc<WatchGet>('watch.get', { watch_id: watchID })).notifications.filter((n) => n.condition === 'needs_you')

/** Gives the server time to record a notice it should not, then counts. */
async function settledNeeds(member: Member, watchID: string): Promise<Notice[]> {
  await new Promise((resolve) => setTimeout(resolve, 5_000))
  return needs(member, watchID)
}

async function allow(page: Page, member: Member, runID: string) {
  await page.goto(`${member.url}&run=${runID}`)
  await page.getByRole('toolbar', { name: 'Run actions' }).getByRole('button', { name: 'Answer' }).click()
  await page.getByRole('button', { name: 'Allow', exact: true }).first().click()
  await expect(page.getByRole('log', { name: 'Session' }).getByText(/permission: allow/).last()).toBeVisible({ timeout: 60_000 })
}

test('with every tab closed a need is noticed once, an answered need never again, and push says no device', async ({ browser, aether }, testInfo) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  await aether.installACPMock(alice, await memberID(alice))
  const runID = await launch(alice, 'demo')

  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  let page = await context.newPage()
  await page.goto(`${alice.url}&run=${runID}`)
  await expect(page.getByRole('log', { name: 'Session' }).getByText('pong')).toBeVisible({ timeout: 60_000 })
  await page.getByRole('toolbar', { name: 'Run actions' }).getByRole('button', { name: 'More', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Watch…' }).click()
  await page.getByRole('dialog', { name: /^Watch / }).getByRole('button', { name: 'Watch', exact: true }).click()
  const receipt = page.getByText(/^Watching run .* for: needs you, finished\./)
  await expect(receipt).toContainText('in-app only')
  const { watches } = await alice.api.rpc<{ watches: { id: string }[] }>('watch.list', { run_id: runID })
  const watchID = watches[0].id

  await page.close()
  expect(context.pages()).toHaveLength(0)
  await alice.api.rpc('run.inject', { run_id: runID, message: 'ask permission', idempotency_key: `durable-1-${runID}` })
  await expect.poll(async () => (await needs(alice, watchID)).length, { timeout: 60_000 }).toBe(1)
  const settled = await settledNeeds(alice, watchID)
  expect(settled).toHaveLength(1)
  const [first] = settled
  expect(first.deliveries.find((d) => d.channel === 'in_app')?.status).toBe('delivered')
  expect(first.deliveries.find((d) => d.channel === 'push')?.status).toBe('no_destination')

  page = await context.newPage()
  await page.goto(`${alice.url}&page=settings&id=watches&watch=${watchID}`)
  const notices = page.getByRole('dialog').getByRole('region', { name: 'Notifications' })
  await expect(notices.getByRole('list', { name: 'Delivery' })).toHaveCount(1)
  await expect(notices.getByRole('list', { name: 'Delivery' })).toContainText('In-app')
  await testInfo.attach('notice recorded with every tab closed', { body: await page.screenshot(), contentType: 'image/png' })

  await allow(page, alice, runID)
  await expect.poll(async () => (await alice.api.rpc<WatchGet>('watch.get', { watch_id: watchID })).watch.observed.need ?? '').toBe('')
  expect(await settledNeeds(alice, watchID)).toHaveLength(1)

  await alice.api.rpc('run.inject', { run_id: runID, message: 'ask permission', idempotency_key: `durable-2-${runID}` })
  await expect.poll(async () => (await needs(alice, watchID)).length, { timeout: 60_000 }).toBe(2)
  await context.close()
})

test('a watch survives a server crash without repeating a notice and notices the next need once', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  await aether.installACPMock(alice, await memberID(alice))
  const runID = await launch(alice, 'demo')
  const { watch } = await alice.api.rpc<{ watch: { id: string } }>('watch.create', { target: { kind: 'run', run_id: runID } })
  await alice.api.rpc('run.inject', { run_id: runID, message: 'ask permission', idempotency_key: `crash-1-${runID}` })
  await expect.poll(async () => (await needs(alice, watch.id)).length, { timeout: 60_000 }).toBe(1)

  // The crash ends the turn, so the permission request is gone and the run
  // waits for a message: one new need, and nothing for the state the run
  // was in before the scheduler reconciled it.
  await aether.server.restart()
  await expect.poll(async () => {
    try {
      return (await needs(alice, watch.id)).length
    } catch {
      return -1
    }
  }, { timeout: 60_000, message: 'the restarted server notices the interrupted turn' }).toBe(2)
  const afterCrash = await settledNeeds(alice, watch.id)
  expect(afterCrash.map((n) => n.body)).toEqual(['Turn interrupted, send a message to continue', 'Permission requested: rm -rf build'])
  expect((await alice.api.rpc<WatchGet>('watch.get', { watch_id: watch.id })).watch.state).toBe('active')

  await alice.api.rpc('run.inject', { run_id: runID, message: 'ask permission', idempotency_key: `crash-2-${runID}` })
  await expect.poll(async () => (await needs(alice, watch.id)).length, { timeout: 60_000 }).toBe(3)
  expect(await settledNeeds(alice, watch.id)).toHaveLength(3)

  await page.goto(`${alice.url}&page=settings&id=watches&watch=${watch.id}`)
  await expect(page.getByRole('dialog').getByRole('region', { name: 'Notifications' }).getByRole('list', { name: 'Delivery' })).toHaveCount(3)
  await testInfo.attach('watch after a server crash', { body: await page.screenshot(), contentType: 'image/png' })
})

const hhmm = (at: Date) => at.toISOString().slice(11, 16)

test('quiet hours hold a push and release it, and a removed member hears nothing more', async ({ browser, aether }, testInfo) => {
  test.setTimeout(8 * 60 * 1000)
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  await aether.installACPMock(alice, await memberID(alice))
  const invite = await aether.invite(alice)
  const bob = await aether.member('bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite, name: 'bob' })
  const bobID = await memberID(bob)
  const runID = await launch(alice, 'demo')
  const own = (await alice.api.rpc<{ watch: { id: string } }>('watch.create', { target: { kind: 'run', run_id: runID } })).watch

  // A device whose push service never resolves: the server must say the
  // push was held and then tried, never that it was delivered.
  await bob.api.rpc('push.subscribe', {
    endpoint: 'https://push.invalid/aether-e2e',
    keys: { p256dh: createECDH('prime256v1').generateKeys('base64url'), auth: randomBytes(16).toString('base64url') },
  })
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()
  await page.goto(`${bob.url}&page=settings&id=notifications`)
  await page.getByRole('checkbox', { name: 'Quiet hours' }).click()
  const now = new Date()
  await page.getByLabel('From').fill(hhmm(new Date(now.getTime() - 60 * 60_000)))
  await page.getByLabel('Until').fill(hhmm(new Date(now.getTime() + 2 * 60_000)))
  await page.getByLabel('Time zone').fill('UTC')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect.poll(async () => (await bob.api.rpc<{ quiet_hours?: { tz: string } }>('notify.prefs.get')).quiet_hours?.tz).toBe('UTC')
  await testInfo.attach('quiet hours', { body: await page.screenshot(), contentType: 'image/png' })

  const { watch, receipt } = await bob.api.rpc<{ watch: { id: string }; receipt: string }>('watch.create', { target: { kind: 'run', run_id: runID } })
  expect(receipt).toContain('Push: 1 device.')
  await alice.api.rpc('run.inject', { run_id: runID, message: 'ask permission', idempotency_key: `quiet-${runID}` })
  await expect.poll(async () => (await needs(bob, watch.id)).length, { timeout: 60_000 }).toBe(1)
  const push = async () => (await needs(bob, watch.id))[0].deliveries.find((d) => d.channel === 'push')!
  expect(await push()).toMatchObject({ status: 'held_quiet_hours', detail: expect.stringMatching(/^Until \d\d:\d\d UTC$/) })
  await page.goto(`${bob.url}&page=settings&id=watches&watch=${watch.id}`)
  await expect(page.getByRole('dialog').getByRole('list', { name: 'Delivery' })).toContainText('quiet hours')
  await testInfo.attach('push held for quiet hours', { body: await page.screenshot(), contentType: 'image/png' })

  await expect.poll(async () => (await push()).status, { timeout: 4 * 60_000, intervals: [5_000], message: 'quiet hours end and the push is tried' }).not.toBe('held_quiet_hours')
  const released = await push()
  expect(released.status).not.toBe('delivered')
  testInfo.annotations.push({ type: 'released push', description: JSON.stringify(released) })
  await page.reload()
  await testInfo.attach('push released after quiet hours', { body: await page.screenshot(), contentType: 'image/png' })
  await context.close()

  await alice.api.rpc('member.remove', { member_id: bobID })
  await allow(await (await browser.newContext()).newPage(), alice, runID)
  await alice.api.rpc('run.inject', { run_id: runID, message: 'ask permission', idempotency_key: `removed-${runID}` })
  await expect.poll(async () => (await needs(alice, own.id)).length, { timeout: 60_000, message: 'the need is raised and the owner told' }).toBe(2)
  const db = new DatabaseSync(path.join(aether.server.dataDir, 'aether.db'), { readOnly: true })
  try {
    const rows = db.prepare('SELECT COUNT(*) AS n FROM watches WHERE member_id = ?').get(bobID) as { n: number }
    expect(rows.n).toBe(0)
  } finally {
    db.close()
  }
})
