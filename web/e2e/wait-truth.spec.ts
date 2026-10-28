// What a waiting run shows for each kind of wait evidence, in the run list,
// the sidebar and the run header, with the checked time aging live.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { launchWaits, missing, showEvidence, spec, tasks, undelivered, unreadable } from './waits'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a waiting run says what Aether last read, never a status it could not read', async ({ page, aether }) => {
  const { alice, ids } = await launchWaits(aether)
  await showEvidence(page, ids)
  await page.clock.install()
  await page.setViewportSize({ width: 1440, height: 900 })
  const url = new URL(alice.url)
  url.searchParams.set('page', 'overview')
  await page.goto(url.toString())

  const row = (task: string) =>
    page.getByRole('main').locator('div', { has: page.getByRole('button', { name: task, exact: true }) }).filter({ has: page.locator('time') }).last()
  const checked = row(tasks.condition)
  await expect(checked).toContainText(`Waiting on ${spec}: open, 3 checks running · checked 2m ago`, { timeout: 30_000 })
  await expect(checked.getByText(/^Waiting on /)).toHaveAttribute('title', /Last read .* The agent reported: CI on #582\./)
  await expect(checked.locator('[data-slot="status-dot"]').first()).toHaveAttribute('data-tone', 'waiting')

  const unknown = row(tasks.unknown)
  await expect(unknown).toContainText(new RegExp(`Waiting on ${spec}: status unknown since \\d{1,2}:\\d{2}.* — ${unreadable.replace(/[()]/g, '\\$&')}`))
  await expect(unknown).not.toContainText('checked')
  await expect(unknown.locator('[data-slot="status-dot"]').first()).toHaveAttribute('data-tone', 'wait-unknown')

  const fault = row(tasks.fault)
  await expect(fault).toContainText(`Wait fault: ${spec}: error, waking the agent — ${missing}`)
  await expect(fault.locator('[data-slot="status-dot"]').first()).toHaveAttribute('data-tone', 'wait-fault')

  const broken = row(tasks.broken)
  await expect(broken).toContainText(`Wait broken: ${spec} resolved, but the agent was not woken · reply to continue — ${undelivered}`)
  await expect(broken.getByText('Needs you', { exact: true })).toBeVisible()

  const stale = row(tasks.stale)
  await expect(stale).toContainText('Reported waiting on CI on #590, no update for 32m')
  await expect(stale.getByText('Stale wait', { exact: true })).toBeVisible()
  await expect(stale).not.toContainText('Turn ended')

  // Only the broken wait asks for a person; the others are quiet.
  const sidebar = page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' })
  await expect(sidebar.getByRole('button', { name: new RegExp(`^Needs you · .*${tasks.broken}`) })).toBeVisible()
  for (const task of [tasks.condition, tasks.unknown, tasks.fault, tasks.stale]) {
    await expect(sidebar.getByRole('button', { name: new RegExp(task) })).not.toHaveAccessibleName(/^Needs you/)
  }
  await expect(sidebar.getByRole('button', { name: new RegExp(`${tasks.unknown} · Waiting on .*status unknown since`) })).toBeVisible()
  await page.screenshot({ path: test.info().outputPath('desktop-run-list.png') })

  await sidebar.getByRole('button', { name: new RegExp(tasks.unknown) }).hover()
  await expect(page.getByRole('tooltip')).toContainText(unreadable)
  await page.screenshot({ path: test.info().outputPath('desktop-sidebar-unknown.png') })
  await page.mouse.move(900, 600)

  // The checked time ages without a new read.
  await page.clock.fastForward('03:00')
  await expect(checked).toContainText('checked 5m ago')

  for (const [kind, words] of [
    ['condition', `Waiting on ${spec}: open, 3 checks running · checked 5m ago`],
    ['unknown', `Waiting on ${spec}: status unknown since`],
    ['fault', `Wait fault: ${spec}: error, waking the agent — ${missing}`],
    ['broken', `${undelivered}`],
    ['stale', 'Reported waiting on CI on #590, no update for 32m'],
  ] as const) {
    await sidebar.getByRole('button', { name: new RegExp(tasks[kind]) }).click()
    await expect(page.getByRole('heading', { name: tasks[kind], exact: true })).toBeVisible()
    await expect(page.getByRole('main').getByText(words, { exact: false }).first()).toBeVisible()
    await page.screenshot({ path: test.info().outputPath(`desktop-header-${kind}.png`) })
  }
  await expect(page.getByRole('button', { name: 'Workflow: Stale wait' })).toBeVisible()
})
