// The terminal on a phone, against a real server: an Enhanced session's
// Terminal tab opens a shell, the grid fits the screen's width, nothing
// raises the keyboard until the terminal is tapped, and the key row's sticky
// Ctrl reaches the shell as a control character.

import type { Page, TestInfo } from '@playwright/test'

import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, shrinkToKeyboardHeight, test } from './mobile'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

const rows = (page: Page) => page.locator('[data-slot="shell-terminal"] .xterm-rows')

/** How far the terminal's own pane, and the page, scroll sideways. */
async function sideways(page: Page) {
  return page.locator('[data-slot="shell-terminal"] .xterm').evaluate((xterm) => {
    const host = xterm.parentElement!
    return {
      pane: host.scrollWidth - host.clientWidth,
      page: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    }
  })
}

const keyboardFocused = (page: Page) =>
  page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea') ?? false)

async function shoot(page: Page, testInfo: TestInfo, name: string) {
  await testInfo.attach(name, { body: await page.screenshot(), contentType: 'image/png' })
}

test('an Enhanced session opens a shell that fits a phone and takes a sticky Ctrl', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 360, height: 780 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  await aether.installACPMock(alice, await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'mock',
    mode: 'acp',
    task: 'demo',
  })

  await page.goto(`${alice.url}&run=${run.id}`)
  await expect(page.getByRole('log', { name: 'Chat' }).getByText('pong')).toBeVisible({ timeout: 60_000 })
  await page.getByRole('tab', { name: 'Terminal', exact: true }).tap()

  // No "No agent terminal": the tab opens a shell in the session's container.
  await expect(rows(page)).toContainText(/[$#]/, { timeout: 30_000 })
  await expect(page.getByText('No agent terminal')).toBeHidden()
  expect(await keyboardFocused(page)).toBe(false)
  await expect.poll(() => sideways(page)).toEqual({ pane: 0, page: 0 })
  await shoot(page, testInfo, '360 idle')

  const keys = page.getByRole('toolbar', { name: 'Terminal keys' })
  for (const name of ['Esc', 'Tab', 'Left', 'Up', 'Enter']) {
    expect((await keys.getByRole('button', { name, exact: true }).boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(44)
  }
  const row = await keys.evaluate((toolbar) => toolbar.scrollWidth - toolbar.clientWidth)
  expect(row).toBe(0)

  // A key from the row goes through without raising the keyboard.
  await keys.getByRole('button', { name: 'Enter', exact: true }).tap()
  expect(await keyboardFocused(page)).toBe(false)

  await page.locator('[data-slot="shell-terminal"] .xterm-screen').tap()
  await expect.poll(() => keyboardFocused(page)).toBe(true)
  const restore = await shrinkToKeyboardHeight(page)
  await page.keyboard.type('sleep 30')
  await keys.getByRole('button', { name: 'Enter', exact: true }).tap()
  await keys.getByRole('button', { name: 'Ctrl', exact: true }).tap()
  await expect(keys.getByRole('button', { name: 'Ctrl', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await page.keyboard.type('c')
  await expect(keys.getByRole('button', { name: 'Ctrl', exact: true })).toHaveAttribute('aria-pressed', 'false')
  // Only an interrupted sleep lets the next command answer this quickly.
  await page.keyboard.type('echo after-$((40+2))')
  await keys.getByRole('button', { name: 'Enter', exact: true }).tap()
  await expect(rows(page)).toContainText('after-42', { timeout: 10_000 })
  await shoot(page, testInfo, '360 keyboard')

  await keys.getByRole('button', { name: 'More keys' }).tap()
  await expect(page.getByRole('dialog', { name: 'More keys' })).toBeVisible()
  await shoot(page, testInfo, '360 more keys')
  await page.getByRole('dialog', { name: 'More keys' }).getByRole('button', { name: 'PgUp' }).tap()
  await expect(page.getByRole('dialog', { name: 'More keys' })).toBeHidden()
  await restore()

  await page.setViewportSize({ width: 430, height: 932 })
  await expect.poll(() => sideways(page)).toEqual({ pane: 0, page: 0 })
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
  await shoot(page, testInfo, '430 idle')
  await page.locator('[data-slot="shell-terminal"] .xterm-screen').tap()
  const restoreWide = await shrinkToKeyboardHeight(page)
  await expect.poll(() => sideways(page)).toEqual({ pane: 0, page: 0 })
  await shoot(page, testInfo, '430 keyboard')
  await restoreWide()
})
