// Lanes under a real pointer, against a real server and gateway. The
// sessions and their mail are answered in the browser: what is under test is
// how the view follows the pointer, not how sessions start, and a lane
// layout needs sessions in known states at known times.

import type { Page } from '@playwright/test'
import { type Aether, expect, test } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

const minute = 60_000

/**
 * Alice's workspace with three working sessions, Lead, Reviewer and Tester,
 * newest first, and one message from Lead to Tester. `between` sessions go
 * between Reviewer and Tester, to push Tester below the fold.
 */
async function setUp(page: Page, aether: Aether, between = 0) {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('shop'), 'shop')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspace = workspaces[0].id
  const member = await memberID(alice)
  const now = Date.now()
  const iso = (minutesAgo: number) => new Date(now - minutesAgo * minute).toISOString()
  const run = (id: string, task: string, minutesAgo: number) => ({
    id, workspace_id: workspace, member_id: member, account_member_id: member, task, harness: 'claude', mode: 'tui',
    status: 'running', state: 'working', status_seq: 0, branch: `aether/${id}`, created_at: iso(minutesAgo), started_at: iso(minutesAgo), finished_at: null,
  })
  const runs = [
    run('run-lanes-lead', 'Lead the checkout rewrite', 20),
    run('run-lanes-reviewer', 'Review the checkout rewrite', 30),
    ...Array.from({ length: between }, (_, i) => run(`run-lanes-helper-${i}`, `Helper ${i + 1}`, 31 + i)),
    run('run-lanes-tester', 'Test the checkout rewrite', 40 + between),
  ]
  const mail = {
    id: 'evt-lanes-mail', seq: 1, time: iso(10), workspace_id: workspace, run_id: 'run-lanes-lead', actor_id: '', type: 'coord.message',
    payload: { message_id: 'msg-lanes-1', workspace_id: workspace, from_run_id: 'run-lanes-lead', to_run_id: 'run-lanes-tester', kind: 'message' },
  }
  await page.route('**/api/v1/run.list', (route) => route.fulfill({ json: { runs } }))
  await page.route('**/api/v1/workspace.timeline', (route) => route.fulfill({ json: { events: [mail], next_seq: 2, more: false } }))
  await page.goto(alice.url)
  // The view choice is kept per workspace; a click before the workspace settles can be undone.
  const tree = page.getByRole('tree', { name: 'Session lanes' })
  await expect(async () => {
    await page.getByRole('tab', { name: 'Lanes' }).click()
    await expect(tree.getByRole('treeitem').first()).toBeVisible({ timeout: 2000 })
  }).toPass()
  return tree
}

test('a lane popover keeps its lane while the pointer travels to it and rests on it', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const tree = await setUp(page, aether)

  const lead = tree.getByRole('treeitem', { name: /^Lead the checkout rewrite/ })
  const links = page.locator('[data-lane-edges] [data-edge="message"]')
  await expect(lead).toBeVisible()
  await expect(links).toHaveCount(0)

  const box = (await lead.boundingBox())!
  const start = { x: box.x + 400, y: box.y + box.height / 2 }
  await page.mouse.move(start.x, start.y)
  await expect(links).toHaveCount(1)
  await page.mouse.click(start.x, start.y)
  const card = page.getByRole('dialog', { name: /^Lead the checkout rewrite/ })
  await expect(card).toBeVisible()

  // The card opens below Lead, over Reviewer, which has no mail: Reviewer
  // taking the focus would take Lead's link away.
  const target = (await card.boundingBox())!
  const reviewerLane = tree.getByRole('treeitem', { name: /^Review the checkout rewrite/ })
  const reviewer = (await reviewerLane.boundingBox())!
  expect(target.y).toBeLessThan(reviewer.y + reviewer.height)
  await page.mouse.move(target.x + 40, reviewer.y + reviewer.height / 2, { steps: 12 })
  await expect(links).toHaveCount(1)
  await page.mouse.move(target.x + 40, target.y + target.height / 2, { steps: 12 })
  await expect(card).toBeVisible()
  await expect(lead).toHaveAttribute('aria-selected', 'true')
  await expect(links).toHaveCount(1)
  const band = page.locator('[data-lane-highlight="hover"]')
  await expect(band).toHaveCount(0)

  // Off the card, the lane under the pointer shows its hover band; the card keeps Lead's link.
  await page.mouse.move(reviewer.x + 100, reviewer.y + reviewer.height / 2, { steps: 4 })
  await expect(band).toHaveCount(1)
  await expect(links).toHaveCount(1)

  // Closed by Escape with the pointer outside the lanes, focus is back on
  // Lead and nothing else holds the hover: the keyboard moves the focus.
  await page.mouse.move(5, 795, { steps: 4 })
  await page.keyboard.press('Escape')
  await expect(card).toBeHidden()
  await expect(lead).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(reviewerLane).toBeFocused()
  await expect(band).toHaveCount(1)
  await expect(links).toHaveCount(0)

  // Closed with the pointer on Reviewer, Reviewer takes the hover at once.
  await page.mouse.click(start.x, start.y)
  await expect(card).toBeVisible()
  await page.mouse.move(reviewer.x + 100, reviewer.y + reviewer.height / 2, { steps: 4 })
  await page.keyboard.press('Escape')
  await expect(card).toBeHidden()
  await expect(band).toHaveCount(1)
  await expect(links).toHaveCount(0)
})

test('one click on another lane moves its card there', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const tree = await setUp(page, aether)
  const lead = tree.getByRole('treeitem', { name: /^Lead the checkout rewrite/ })
  await expect(lead).toBeVisible()
  const box = (await lead.boundingBox())!
  await page.mouse.click(box.x + 120, box.y + box.height / 2)
  await expect(page.getByRole('dialog', { name: /^Lead the checkout rewrite/ })).toBeVisible()
  const reviewer = (await tree.getByRole('treeitem', { name: /^Review the checkout rewrite/ }).boundingBox())!
  await page.mouse.click(reviewer.x + 120, reviewer.y + reviewer.height / 2)
  await expect(page.getByRole('dialog', { name: /^Review the checkout rewrite/ })).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(1)
})

// The hover waits on a timer while the pointer heads for a pinned button,
// so these cases hold the page's clock: nothing hands over between steps.
async function holdClock(page: Page) {
  await page.clock.pauseAt(await page.evaluate(() => Date.now() + 1000))
}

test("a lane's pinned button stays while the pointer crosses other lanes to reach it", async ({ page, aether }) => {
  await page.setViewportSize({ width: 1280, height: 600 })
  await page.clock.install()
  const tree = await setUp(page, aether, 16)
  const lead = tree.getByRole('treeitem', { name: /^Lead the checkout rewrite/ })
  await expect(lead).toBeVisible()
  const box = (await lead.boundingBox())!
  await page.mouse.move(box.x + 300, box.y + box.height / 2)
  const pinned = page.locator('[data-mail-pinned]')
  await expect(pinned).toHaveText(/Test the checkout rewrite/)

  // A straight path from Lead down to the button, over a dozen lanes with no mail.
  await holdClock(page)
  const button = (await pinned.boundingBox())!
  await page.mouse.move(button.x + button.width / 2, button.y + button.height / 2, { steps: 20 })
  await expect(pinned).toHaveText(/Test the checkout rewrite/)
  await page.mouse.down()
  await page.mouse.up()
  await page.clock.resume()
  await expect(tree.getByRole('treeitem', { name: /^Test the checkout rewrite/ })).toBeFocused()
})

test('a lane the pointer rests on takes the hover even on the way to a pinned button', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1280, height: 600 })
  await page.clock.install()
  const tree = await setUp(page, aether, 16)
  const lead = tree.getByRole('treeitem', { name: /^Lead the checkout rewrite/ })
  await expect(lead).toBeVisible()
  const box = (await lead.boundingBox())!
  await page.mouse.move(box.x + 300, box.y + box.height / 2)
  const pinned = page.locator('[data-mail-pinned]')
  await expect(pinned).toBeVisible()
  const button = (await pinned.boundingBox())!
  const helper = (await tree.getByRole('treeitem', { name: /^Helper 3,/ }).boundingBox())!
  const x = box.x + 300 + ((button.x - box.x - 300) * (helper.y - box.y)) / (button.y - box.y)
  await holdClock(page)
  await page.mouse.move(x, helper.y + helper.height / 2, { steps: 10 })
  const band = page.locator('[data-lane-highlight="hover"]')
  await expect(pinned).toBeVisible()
  expect((await band.boundingBox())?.y).toBe(box.y)
  await page.clock.runFor(200)
  await expect(pinned).toBeHidden()
  expect((await band.boundingBox())?.y).toBe(helper.y)
})

test('a pinned button moves to its lane and closes the open card', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1280, height: 600 })
  const tree = await setUp(page, aether, 16)
  const lead = tree.getByRole('treeitem', { name: /^Lead the checkout rewrite/ })
  await expect(lead).toBeVisible()
  const box = (await lead.boundingBox())!
  await page.mouse.click(box.x + 300, box.y + box.height / 2)
  await expect(page.getByRole('dialog', { name: /^Lead the checkout rewrite/ })).toBeVisible()
  await page.locator('[data-mail-pinned]').click()
  await expect(tree.getByRole('treeitem', { name: /^Test the checkout rewrite/ })).toBeFocused()
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('dragging the track pans and closes the open card', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const tree = await setUp(page, aether)
  const lead = tree.getByRole('treeitem', { name: /^Lead the checkout rewrite/ })
  await expect(lead).toBeVisible()
  const box = (await lead.boundingBox())!
  await page.mouse.click(box.x + 600, box.y + box.height / 2)
  await expect(page.getByRole('dialog')).toHaveCount(1)
  await page.mouse.move(box.x + 600, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(box.x + 800, box.y + box.height / 2, { steps: 8 })
  await page.mouse.up()
  await expect(page.getByRole('button', { name: 'Now', exact: true })).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
})
