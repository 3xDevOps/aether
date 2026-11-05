// A session on a phone, against a real server: it opens full screen over the
// tab bar, an Enhanced session lands on Chat and a Standard one on Terminal,
// the chrome above the transcript stays small, and a message goes out from
// the composer pinned to the bottom while the header folds to one row.

import type { Page } from '@playwright/test'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, shrinkToKeyboardHeight, test } from './mobile'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

const overflowX = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)

/** Folded, the tabs stay mounted for their panels' labels inside a 1px visually hidden row. */
const tabRowHeight = (page: Page) =>
  page.getByRole('tablist', { name: 'Session views' }).locator('..').evaluate((row) => row.getBoundingClientRect().height)

test('an Enhanced session opens on Chat and takes a message from the pinned composer', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  await alice.api.rpc('run.launch', { workspace_id: workspaces[0].id, harness: 'mock', mode: 'acp', task: 'demo' })

  const log = page.getByRole('log', { name: 'Chat' })
  const views = page.getByRole('tablist', { name: 'Session views' })
  for (const width of [360, 430]) {
    await page.setViewportSize({ width, height: 800 })
    await page.goto(alice.url)
    await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Sessions' }).tap()
    await page.getByRole('main').getByRole('region', { name: 'Sessions' }).getByRole('button', { name: /demo/ }).tap()

    await expect(views.getByRole('tab', { name: 'Chat' })).toHaveAttribute('aria-selected', 'true')
    await expect(views.getByRole('tab')).toHaveText([/^Chat/, /^Terminal/, /^Changes/])
    await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeHidden()
    await expect(log.getByText('pong').first()).toBeVisible({ timeout: 180_000 })
    expect(await overflowX(page)).toBe(0)
    // Before this layout a session spent 244px on chrome at 390px wide.
    const chrome = await log.evaluate((element) => element.getBoundingClientRect().top)
    expect(chrome).toBeLessThan(122)

    await page.getByRole('button', { name: 'Back' }).tap()
    await expect(page.getByRole('main').getByRole('region', { name: 'Sessions' })).toBeVisible()
  }

  await page.getByRole('main').getByRole('region', { name: 'Sessions' }).getByRole('button', { name: /demo/ }).tap()
  const restore = await shrinkToKeyboardHeight(page)
  const box = page.getByRole('combobox', { name: 'Message the agent' })
  await box.tap()
  await expect.poll(() => tabRowHeight(page)).toBeLessThanOrEqual(1)
  await expect(page.getByRole('heading', { name: 'demo', exact: true })).toBeInViewport()
  await expect(box).toBeInViewport({ ratio: 1 })
  await box.fill('hello from the phone')
  await page.getByRole('button', { name: 'Send', exact: true }).tap()
  await expect(log.getByText('hello from the phone')).toBeVisible()
  await expect(log.getByText('pong')).toHaveCount(2, { timeout: 60_000 })
  await restore()

  // iOS shrinks only the visual viewport, so the composer rides --keyboard-inset above its keyboard.
  // useKeyboardInset rewrites the property on each visualViewport resize, and the restored
  // viewport's resize lands asynchronously: set it only once that has been handled.
  const height = page.viewportSize()!.height
  await expect.poll(() => page.evaluate(() => window.visualViewport?.height)).toBe(height)
  await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
  await page.evaluate(() => document.documentElement.style.setProperty('--keyboard-inset', '200px'))
  await expect.poll(async () => {
    const rect = await box.boundingBox()
    return rect ? rect.y + rect.height : Infinity
  }).toBeLessThanOrEqual(height - 200)
})

test('a link to a view wins on a phone, and Back returns to Needs you', async ({ page, aether }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id, harness: 'mock', mode: 'acp', task: 'demo',
  })
  const views = page.getByRole('tablist', { name: 'Session views' })

  await page.goto(`${alice.url}&run=${run.id}&view=changes`)
  await expect(views.getByRole('tab', { name: 'Changes' })).toHaveAttribute('aria-selected', 'true')

  // The first prompt carries the run's instructions, so the mock asks only for a prompt sent later.
  await views.getByRole('tab', { name: 'Chat' }).tap()
  await expect(page.getByRole('log', { name: 'Chat' }).getByText('pong').first()).toBeVisible({ timeout: 180_000 })
  await page.getByRole('combobox', { name: 'Message the agent' }).fill('ask permission')
  await page.getByRole('button', { name: 'Send', exact: true }).tap()
  // Leaving before the agent asks would drop the prompt with the page.
  await expect(page.getByText('Answer the request above to continue.')).toBeVisible({ timeout: 60_000 })

  await page.goto(alice.url)
  const card = page.getByRole('list', { name: 'Needs you' }).getByRole('group').filter({ hasText: 'Permission requested' })
  await card.getByRole('button', { name: 'Review' }).tap({ timeout: 60_000 })
  await expect(views.getByRole('tab', { name: 'Chat' })).toHaveAttribute('aria-selected', 'true')
  await page.getByRole('button', { name: 'Back' }).tap()
  await expect(page.getByRole('list', { name: 'Needs you' })).toBeVisible()
})

test('a Standard session opens on Terminal', async ({ page, aether }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task: 'write the result file',
  })

  await page.goto(`${alice.url}&run=${run.id}`)
  const views = page.getByRole('tablist', { name: 'Session views' })
  await expect(views.getByRole('tab', { name: 'Terminal' })).toHaveAttribute('aria-selected', 'true')
  await expect(views.getByRole('tab')).toHaveText([/^Terminal/, /^Chat/, /^Changes/])
  expect(await overflowX(page)).toBe(0)
  // A link has no page behind it in the app, so Back lands on the session list.
  await page.getByRole('button', { name: 'Back' }).tap()
  await expect(page.getByRole('main').getByRole('region', { name: 'Sessions' })).toBeVisible()
})

test('Back leaves a session in one step, a reopened one lands on Chat, and the tabs return with the keyboard down', async ({ page, aether }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  await alice.api.rpc('run.launch', { workspace_id: workspaces[0].id, harness: 'mock', mode: 'acp', task: 'demo' })
  const sessions = page.getByRole('main').getByRole('region', { name: 'Sessions' })
  const views = page.getByRole('tablist', { name: 'Session views' })
  const log = page.getByRole('log', { name: 'Chat' })

  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Sessions' }).tap()
  await sessions.getByRole('button', { name: /demo/ }).tap()
  await expect(log.getByText('pong').first()).toBeVisible({ timeout: 180_000 })
  await views.getByRole('tab', { name: 'Terminal' }).tap()
  await views.getByRole('tab', { name: /^Changes/ }).tap()
  await views.getByRole('tab', { name: 'Chat' }).tap()

  const box = page.getByRole('combobox', { name: 'Message the agent' })
  await box.fill('subagents')
  await page.getByRole('button', { name: 'Send', exact: true }).tap()
  await expect(log.getByText('All three subagents finished.')).toBeVisible({ timeout: 30_000 })
  await log.getByRole('button', { name: /^Delegated 3 tasks/ }).tap()
  await log.getByRole('button', { name: 'Map the billing module, Explore, Done' }).tap()
  const inspector = page.getByRole('dialog', { name: 'Subagent' })
  await inspector.getByRole('button', { name: 'Close subagent' }).tap()
  await expect(inspector).toBeHidden()

  await page.getByRole('button', { name: 'Back' }).tap()
  await expect(sessions).toBeVisible()

  await sessions.getByRole('button', { name: /demo/ }).tap()
  await views.getByRole('tab', { name: 'Terminal' }).tap()
  await page.getByRole('button', { name: 'Back' }).tap()
  await sessions.getByRole('button', { name: /demo/ }).tap()
  await expect(views.getByRole('tab', { name: 'Chat' })).toHaveAttribute('aria-selected', 'true')

  // Android's Back and iOS's Done drop the keyboard without a blur.
  const restore = await shrinkToKeyboardHeight(page)
  await box.tap()
  await expect.poll(() => tabRowHeight(page)).toBeLessThanOrEqual(1)
  await restore()
  await expect(box).toBeFocused()
  await expect.poll(() => tabRowHeight(page)).toBeGreaterThanOrEqual(44)
})
