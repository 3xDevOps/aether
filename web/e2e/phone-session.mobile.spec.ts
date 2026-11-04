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
  await expect(views).toBeHidden()
  await expect(page.getByRole('heading', { name: 'demo', exact: true })).toBeInViewport()
  await expect(box).toBeInViewport({ ratio: 1 })
  await box.fill('hello from the phone')
  await page.getByRole('button', { name: 'Send', exact: true }).tap()
  await expect(log.getByText('hello from the phone')).toBeVisible()
  await expect(log.getByText('pong')).toHaveCount(2, { timeout: 60_000 })
  await restore()

  // iOS shrinks only the visual viewport, so the composer rides --keyboard-inset above its keyboard.
  await page.evaluate(() => document.documentElement.style.setProperty('--keyboard-inset', '200px'))
  const height = page.viewportSize()!.height
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
