// The assistant opened beside a run, at a window wide enough to dock it and
// at one too narrow. Either way the run keeps a readable width and stays
// usable: Details gives way to an overlay rather than squeezing the run, and
// a floating assistant has no backdrop over the run.

import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

const task = 'keep the run readable beside the assistant'

/** minReadableWidth in src/lib/breakpoints.ts. */
const readable = 560

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

function mainWidth(page: Page): Promise<number> {
  return page.locator('#main').evaluate((element) => element.getBoundingClientRect().width)
}

test('the assistant beside a run leaves the run readable and usable', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task,
    mode: 'headless',
  })

  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto(`${alice.url}&run=${run.id}`)
  await expect(page.getByRole('heading', { level: 1, name: task, exact: true })).toBeVisible()

  const toggle = page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: /^Assistant/ })
  const assistant = page.getByRole('complementary', { name: 'Assistant' })
  const inlineDetails = page.getByRole('complementary', { name: 'Run details' })
  const overlayDetails = page.getByRole('dialog', { name: 'Run details' })
  const views = page.getByRole('tablist', { name: 'Run views' })

  await expect(inlineDetails).toBeVisible()

  await toggle.click()
  await expect(assistant).toBeVisible()
  await expect(assistant.getByRole('heading', { level: 2, name: 'Assistant' })).toBeVisible()
  await expect(inlineDetails).toHaveCount(0)
  expect(await mainWidth(page)).toBeGreaterThanOrEqual(readable)
  const docked = await assistant.boundingBox()
  const main = await page.locator('#main').boundingBox()
  expect(docked!.x).toBeGreaterThanOrEqual(main!.x + main!.width - 1)

  await page.getByRole('button', { name: 'Show details' }).click()
  await expect(overlayDetails).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(overlayDetails).toHaveCount(0)

  await assistant.getByRole('button', { name: 'Close assistant' }).click()
  await expect(assistant).toHaveCount(0)
  await expect(inlineDetails).toBeVisible()

  await page.setViewportSize({ width: 1000, height: 800 })
  await expect(inlineDetails).toHaveCount(0)
  const alone = await mainWidth(page)

  await toggle.click()
  await expect(assistant).toBeVisible()
  await expect(page.getByRole('dialog', { name: 'Assistant' })).toHaveCount(0)
  expect(await mainWidth(page)).toBe(alone)
  expect(alone).toBeGreaterThanOrEqual(readable)

  const changes = views.getByRole('tab', { name: 'Changes' })
  await changes.click()
  await expect(changes).toHaveAttribute('aria-selected', 'true')
  await expect(assistant).toBeVisible()

  await assistant.getByRole('button', { name: 'Close assistant' }).focus()
  await page.keyboard.press('Escape')
  await expect(assistant).toHaveCount(0)
  await expect(toggle).toBeFocused()
  await expect(page.getByRole('heading', { level: 1, name: task, exact: true })).toBeVisible()
})
