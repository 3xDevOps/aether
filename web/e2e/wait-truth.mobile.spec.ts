// The same waits on a 390px phone: the run list, the sidebar drawer and the
// run header each say what Aether last read, with no sideways scroll.

import { dockerReachable } from './harness/server'
import { expect, test } from './mobile'
import { launchWaits, missing, showEvidence, spec, tasks, undelivered, unreadable } from './waits'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')
test.use({ viewport: { width: 390, height: 844 } })

test('a waiting run says what Aether last read on a phone', async ({ page, aether }) => {
  const { alice, ids } = await launchWaits(aether)
  await showEvidence(page, ids)
  const url = new URL(alice.url)
  url.searchParams.set('page', 'overview')
  await page.goto(url.toString())

  const main = page.getByRole('main')
  await expect(main).toContainText(`Waiting on ${spec}: open, 3 checks running · checked 2m ago`, { timeout: 30_000 })
  await expect(main).toContainText(unreadable)
  await expect(main).toContainText(`Wait fault: ${spec}: error, waking the agent — ${missing}`)
  await expect(main).toContainText('Reported waiting on CI on #590, no update for 32m')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: test.info().outputPath('phone-run-list.png'), fullPage: true })

  await page.getByRole('banner').getByRole('button', { name: /^Open sidebar/ }).click()
  const sidebar = page.getByRole('dialog', { name: 'Aether' }).getByRole('region', { name: 'Sessions' })
  await expect(sidebar.getByRole('button', { name: new RegExp(`^Needs you · .*${tasks.broken}`) })).toBeVisible()
  await expect(sidebar.getByRole('button', { name: new RegExp(`${tasks.unknown} · Waiting on .*status unknown since`) })).toBeVisible()
  await page.screenshot({ path: test.info().outputPath('phone-sidebar.png') })

  for (const [kind, words] of [
    ['condition', `Waiting on ${spec}: open`],
    ['unknown', unreadable],
    ['fault', missing],
    ['broken', undelivered],
  ] as const) {
    const run = new URL(alice.url)
    run.searchParams.set('run', ids[kind])
    await page.goto(run.toString())
    await expect(page.getByRole('heading', { name: tasks[kind], exact: true })).toBeVisible({ timeout: 30_000 })
    await expect(page.getByRole('main').locator('p', { hasText: words })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: test.info().outputPath(`phone-header-${kind}.png`) })
  }
})
