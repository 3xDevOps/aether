// Lanes' bars and marks measured in a real browser, against a real server
// and gateway, with the sessions and their mail answered in the browser:
// what is under test is what a person can read and hit.

import { expect, test } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

test('bar text ends in an ellipsis clear of the Needs you tag, and lanes and marks are easy to hit', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('shop'), 'shop')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspace = workspaces[0].id
  const member = await memberID(alice)
  const iso = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString()
  const base = { workspace_id: workspace, member_id: member, account_member_id: member, harness: 'claude', mode: 'tui', status_seq: 0 }
  const runs = [
    {
      ...base, id: 'run-asks', task: 'Add integration tests for SSO login', status: 'needs-attention', state: 'needs-you',
      reason: 'needs input: Should the SSO tests hit the staging identity provider, a local mock, or both, and which accounts may they use?',
      created_at: iso(300), started_at: iso(300), finished_at: null,
    },
    { ...base, id: 'run-quick', task: 'Tidy CODEOWNERS', status: 'completed', state: 'completed', created_at: iso(30), started_at: iso(30), finished_at: iso(28) },
  ]
  const mail = {
    id: 'evt-mail', seq: 1, time: iso(20), workspace_id: workspace, run_id: 'run-quick', actor_id: '', type: 'coord.message',
    payload: { message_id: 'msg-1', workspace_id: workspace, from_run_id: 'run-quick', to_run_id: 'run-asks', kind: 'message' },
  }
  await page.route('**/api/v1/run.list', (route) => route.fulfill({ json: { runs } }))
  await page.route('**/api/v1/workspace.timeline', (route) => route.fulfill({ json: { events: [mail], next_seq: 2, more: false } }))
  await page.goto(alice.url)
  const tree = page.getByRole('tree', { name: 'Session lanes' })
  await expect(async () => {
    await page.getByRole('tab', { name: 'Lanes' }).click()
    await expect(tree.getByRole('treeitem').first()).toBeVisible({ timeout: 2000 })
  }).toPass()
  await tree.getByRole('treeitem', { name: /^1 finished/ }).click()

  const asks = tree.getByRole('treeitem', { name: /^Add integration tests for SSO login/ })
  const tag = asks.locator('[data-lane-track] span.rounded-chip', { hasText: 'Needs you' })
  const text = asks.getByText(/^Agent asks:/)
  await expect(tag).toBeVisible()
  const textBox = (await text.boundingBox())!
  expect(textBox.x + textBox.width).toBeLessThanOrEqual((await tag.boundingBox())!.x)
  expect(await text.evaluate((el) => getComputedStyle(el).textOverflow)).toBe('ellipsis')

  const quick = tree.getByRole('treeitem', { name: /^Tidy CODEOWNERS/ })
  await expect(quick).toBeVisible()
  expect(await quick.evaluate((el) => getComputedStyle(el).cursor)).toBe('pointer')
  // Two minutes on a 6h window is a few pixels: too short for any word, so it shows only its colour.
  await expect(quick.locator('[data-lane-track]').getByText('Completed')).toHaveCount(0)

  // Each mark's target is at least 24px wide and answers a press on its own dot, even facing a neighbour's mark.
  const marks = page.locator('[data-lane-mail] g[data-mail]')
  await expect(marks).toHaveCount(2)
  for (const mark of await marks.all()) {
    expect((await mark.locator('rect[data-mail-control]').boundingBox())!.width).toBeGreaterThanOrEqual(24)
    expect(await mark.evaluate((g) => {
      const dot = g.querySelector('circle')!.getBoundingClientRect()
      return document.elementFromPoint(dot.x + dot.width / 2, dot.y + dot.height / 2)?.parentElement === g
    })).toBe(true)
  }
})
