// An Enhanced run whose agent takes a prompt and makes no progress, against a
// real server with a short --first-event-grace: acpmock's `chatter` turn sends
// only usage and command updates, which are not progress. The run reads
// Possibly stalled, never Needs you or Disconnected, and its Session view offers
// Stop turn and a Resend that asks before it spends tokens.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

test.use({ serverOptions: { args: ['--first-event-grace', '3s', '--poll-interval', '1s'] } })

test('a turn with no progress reads Possibly stalled with its last progress, and stops or resends on request', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0].id, harness: 'mock', mode: 'acp', task: 'demo' })
  const status = async () => (await alice.api.rpc<{ run: { status: string; state?: string; reason?: string; liveness?: { phase?: string; transport: string } } }>('run.get', { run_id: run.id })).run

  await page.goto(`${alice.url}&run=${run.id}`)
  const log = page.getByRole('log', { name: 'Chat' })
  const runs = page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Sessions' })
  const header = page.locator('[data-slot="pane-header"]')
  const callout = page.locator('[data-slot="callout"]').filter({ hasText: 'Possibly stalled' })
  const box = page.getByRole('combobox', { name: 'Message the agent' })
  await expect(log.getByText('pong').first()).toBeVisible({ timeout: 60_000 })

  await box.fill('chatter')
  await box.press('Enter')
  await expect.poll(status, { timeout: 30_000 }).toMatchObject({
    status: 'needs-attention',
    state: 'waiting',
    reason: expect.stringMatching(/^stalled: possibly — no progress for \d+s, awaiting first event, transport open$/),
    liveness: { phase: 'awaiting-first-event', transport: 'open' },
  })
  const words = /No progress for \d+s while awaiting the model's first reply; connection open/
  await expect(runs.getByRole('button', { name: /^Possibly stalled · / })).toBeVisible()
  await expect(header.getByText(words)).toBeVisible()
  await expect(header.locator('[title^="Possibly stalled since"]')).toHaveAttribute('title', /Why: the session stalled\.$/)
  await expect(runs.getByRole('button', { name: /^Needs you/ })).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Needs you' })).toContainText('Nothing is waiting on you.')
  await expect(page.getByText('Disconnected', { exact: true })).toHaveCount(0)
  await expect(callout.getByText(words)).toBeVisible()
  await expect(callout.getByText('awaiting-first-event')).toBeVisible()
  await expect(callout.locator('dt', { hasText: 'Connection' }).locator('+ dd')).toHaveText('open')
  // The demo turn's reply is the agent's last progress.
  await expect(callout.locator('dt', { hasText: 'Last progress' }).locator('+ dd')).toHaveText(/^\d{1,2}:\d{2}.*\(.+ago\)$/)
  await page.screenshot({ path: testInfo.outputPath('possibly-stalled-desktop.png'), animations: 'disabled' })

  await callout.getByRole('button', { name: 'Open trace' }).click()
  await expect(page.getByRole('tree', { name: 'Trace' }).getByRole('treeitem', { name: /^chatter, Turn/ })).toBeVisible()
  await page.getByRole('tab', { name: 'Chat' }).click()

  await callout.getByRole('button', { name: 'Resend last prompt…' }).click()
  const dialog = page.getByRole('alertdialog', { name: 'Resend your last prompt?' })
  await expect(dialog).toContainText('It costs tokens, and the agent may repeat tool actions')
  await expect(dialog.locator('pre')).toHaveText('chatter')
  await expect(dialog).toHaveCSS('opacity', '1')
  await page.screenshot({ path: testInfo.outputPath('possibly-stalled-resend-desktop.png'), animations: 'disabled' })
  await dialog.getByRole('button', { name: 'Keep waiting' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(log.getByText('chatter', { exact: true })).toHaveCount(1)

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(callout).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('possibly-stalled-mobile.png'), animations: 'disabled' })
  await page.setViewportSize({ width: 1440, height: 900 })

  // Confirmed, the prompt queues behind the stalled turn; Stop turn lets it start, and it stalls the same way.
  await callout.getByRole('button', { name: 'Resend last prompt…' }).click()
  await dialog.getByRole('button', { name: 'Resend and spend tokens' }).click()
  await callout.getByRole('button', { name: 'Stop turn' }).click()
  await expect(log.getByText('chatter', { exact: true })).toHaveCount(2, { timeout: 30_000 })
  await expect.poll(status, { timeout: 30_000 }).toMatchObject({ status: 'needs-attention', reason: expect.stringMatching(/^stalled: possibly — /) })
  await expect(callout).toBeVisible()

  await callout.getByRole('button', { name: 'Stop turn' }).click()
  await expect.poll(status).toMatchObject({ status: 'needs-attention', reason: 'turn cancelled' })
  await expect(callout).toHaveCount(0)
  await expect(header.getByText('Turn cancelled')).toBeVisible()
})
