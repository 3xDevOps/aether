// An approval card on a 390px phone against a real gateway and server, with
// the assistant scripted as in assistant-approval-rules.spec.ts: the sheet
// fits the screen, and every row of what a launch starts sits above, and
// scrolls into view before, the button that saves a rule covering it.

import { expect, test } from './fixtures'

test.use({ viewport: { width: 390, height: 844 } })

const wording = 'Always approve launches of Claude Code (Enhanced, model "claude-opus-5-5", effort "high") in api on your account, permission isolated, no fallback, for any task'

test('a launch card shows every row it covers before Always approve, inside the screen', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const card = {
    id: 'conf-1',
    method: 'run.launch',
    summary: 'Launch claude (acp, model claude-opus-5-5, effort high) in api, no fallback, permission isolated: "Reconcile the ledger export with the settlement report"',
    params: { workspace_id: 'ws-scripted', harness: 'claude', mode: 'acp', model: 'claude-opus-5-5', effort: 'high', permission: 'isolated', no_fallback: true, task: 'Reconcile the ledger export with the settlement report and add a regression test for the rounding bug' },
    reason: 'The export and the report disagree by one cent on 14 invoices.',
    created_at: new Date().toISOString(),
    expires_at: new Date(Date.now() + 600_000).toISOString(),
    rule: { wording, suggested: true },
  }
  const status = { state: 'idle', agent: 'claude', agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }], auto_approve: false, suggest_auto_approve: false, share_view: true, approval_chosen: true, confirmations: [card] }
  await page.route('**/api/v1/assistant.status', (route) => route.fulfill({ json: status }))
  await page.routeWebSocket(/\/ws\/assistant/, (ws) => {
    ws.onMessage(() => ws.send([{ ok: true, seq: 0, replay: 0, epoch: 0, live: true, has_control: false, state: { turn_in_flight: false, queued: 0, pending: [], prompt_images: false } }, { status }].map((f) => JSON.stringify(f)).join('\n')))
  })

  await page.goto(alice.url)
  await page.getByRole('button', { name: 'Assistant' }).first().click()
  const sheet = page.getByRole('dialog', { name: 'Assistant' })
  const always = sheet.getByRole('button', { name: wording })
  await expect(always).toBeVisible()
  // The card's expiry bar restarts its width transition on every tick, so a
  // wait for no running animation can last forever on a slow runner; only
  // the sheet's slide-in has to settle before its box is measured.
  let last = ''
  await expect.poll(async () => {
    const now = JSON.stringify(await sheet.boundingBox())
    const settled = now === last
    last = now
    return settled
  }, { intervals: [100] }).toBe(true)

  const box = (await sheet.boundingBox())!
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(390)
  for (const button of [always, sheet.getByRole('button', { name: 'Approve', exact: true }), sheet.getByRole('button', { name: /^Send/ })]) {
    await button.scrollIntoViewIfNeeded()
    const b = (await button.boundingBox())!
    expect(b.x + b.width).toBeLessThanOrEqual(390)
  }

  const rows = sheet.getByRole('region', { name: 'What starts' }).locator('dt')
  expect(await rows.count()).toBeGreaterThan(3)
  await always.scrollIntoViewIfNeeded()
  for (const row of await rows.all()) {
    await expect(row).toBeInViewport()
    expect((await row.boundingBox())!.y).toBeLessThan((await always.boundingBox())!.y)
  }
})
