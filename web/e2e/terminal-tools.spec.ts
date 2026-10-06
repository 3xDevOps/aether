// The environment terminal's own tools, against a real environment container:
// nothing starts until asked for, the font zoom holds across a reload, and
// the find bar searches what the shell actually printed.

import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { OnboardingWizard } from './pages/wizard'

test.skip(!dockerReachable(), 'the environment terminal needs a reachable Docker daemon')

test.use({ viewport: { width: 1600, height: 1000 }, hasTouch: false })

test('the environment terminal opens on request, zooms and finds', async ({ page, browser, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.connect.link(aether.server.addr, { name: 'Alice' })
  await wizard.connect.continue().click()
  await wizard.repository.createFromClone('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.agent.skip().click()
  const nav = (target: Page) => target.getByRole('navigation', { name: 'Aether' })
  await nav(page).getByRole('button', { name: 'Board', exact: true }).click()
  const dock = page.getByRole('region', { name: 'Environment terminal' })
  await expect(page.getByRole('heading', { name: 'Board', exact: true })).toBeVisible()
  await expect(dock).toHaveCount(0)

  await nav(page).getByRole('button', { name: 'Environment', exact: true }).click()
  await expect(dock.getByText('Your environment starts on first open')).toBeVisible()
  await dock.getByRole('button', { name: 'Open', exact: true }).click()
  await expect(dock.getByRole('status')).toBeHidden({ timeout: 60_000 })

  const tools = dock.getByRole('button', { name: 'Terminal tools', exact: true })
  await tools.click()
  const menu = page.getByRole('menu')
  for (const name of [/^Find/, 'Copy selection', 'Copy screen', 'Paste', 'Upload image…']) {
    await expect(menu.getByRole('menuitem', { name })).toBeVisible()
  }
  await page.keyboard.press('Escape')
  await expect(tools).toBeFocused()

  const screen = dock.locator('.xterm-screen')
  await screen.click()
  // Ctrl+Shift+V must stay on xterm's native paste path. This exercises the
  // Windows-sensitive shortcut without depending on navigator.clipboard.readText
  // in the renderer; clipboard-read permission is deliberately not granted.
  await page.context().grantPermissions(['clipboard-write'], {
    origin: new URL(alice.url).origin,
  })
  await page.evaluate(async () => {
    await navigator.clipboard.writeText('printf "\\141ether-native-paste\\n"\n')
  })
  await page.keyboard.press('Control+Shift+V')
  const nativePasteCount = async () => {
    const text = (await dock.locator('.xterm-rows').textContent()) ?? ''
    return text.split('aether-native-paste').length - 1
  }
  await expect.poll(nativePasteCount, { timeout: 30_000 }).toBe(1)
  await page.keyboard.type('echo aether-found-me\n')
  await expect(dock.locator('.xterm-rows')).toContainText('aether-found-me', {
    timeout: 30_000,
  })

  // Zoom moves the live terminal and is remembered, so a reload comes back
  // at the size the member left.
  const fontSize = () =>
    dock.locator('.xterm-rows').evaluate((el) => getComputedStyle(el).fontSize)
  expect(await fontSize()).toBe('12px')
  await page.keyboard.press('Control+Equal')
  await page.keyboard.press('Control+Equal')
  await expect.poll(fontSize).toBe('14px')

  await page.reload()
  await nav(page).getByRole('button', { name: 'Environment', exact: true }).click()
  // The reattach's spinner has not necessarily mounted yet, so wait for the
  // rows the size is read off rather than for the spinner to go.
  await expect(dock.locator('.xterm-rows')).toBeVisible({ timeout: 60_000 })
  await expect.poll(fontSize).toBe('14px')

  // Find selects the match the shell printed; a term that is not there says
  // so rather than failing silently.
  await dock.locator('.xterm-screen').click()
  await page.keyboard.press('Control+Shift+F')
  const find = dock.getByLabel('Find in terminal')
  await find.fill('aether-found-me')
  await find.press('Enter')
  await expect(dock.locator('.xterm-selection div').first()).toBeVisible()

  await find.fill('no-such-output')
  await find.press('Enter')
  await expect(dock.getByText('No matches')).toBeVisible()

  await find.press('Escape')
  await expect(dock.getByLabel('Find in terminal')).toBeHidden()

  // A narrow pane reaches the same tools from the keyboard.
  await page.setViewportSize({ width: 640, height: 800 })
  await tools.focus()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('menuitem', { name: 'Copy screen' })).toBeVisible()
  await page.getByRole('menuitem', { name: /^Find/ }).click()
  await find.fill('aether-found-me')
  await find.press('Enter')
  await expect(dock.locator('.xterm-selection div').first()).toBeVisible()
  await find.press('Escape')
  await page.setViewportSize({ width: 1600, height: 1000 })

  // Touch uses the same menu, with touch-sized rows.
  const touchContext = await browser.newContext({
    hasTouch: true,
    viewport: { width: 1600, height: 1000 },
    storageState: await page.context().storageState(),
  })
  try {
    const touchPage = await touchContext.newPage()
    await touchPage.goto(alice.url)
    const touchDock = touchPage.getByRole('region', { name: 'Environment terminal' })
    await nav(touchPage).getByRole('button', { name: 'Environment', exact: true }).click()
    await expect(touchDock.locator('.xterm-rows')).toBeVisible({ timeout: 60_000 })
    await touchDock.getByRole('button', { name: 'Terminal tools', exact: true }).click()
    const copy = touchPage.getByRole('menuitem', { name: 'Copy selection' })
    await expect(copy).toBeVisible()
    // Polled: the menu opens scaled to 0.98, so an early box is about 43px.
    await expect.poll(async () => Math.round((await copy.boundingBox())?.height ?? 0)).toBeGreaterThanOrEqual(44)
    await touchPage.getByRole('menuitem', { name: /^Find/ }).click()
    const touchFind = touchDock.getByLabel('Find in terminal')
    await touchFind.fill('aether-found-me')
    await touchFind.press('Enter')
    await expect(touchDock.locator('.xterm-selection div').first()).toBeVisible()
    await touchFind.press('Escape')
  } finally {
    await touchContext.close()
  }

  await nav(page).getByRole('button', { name: 'Board', exact: true }).click()
  await expect(dock).toHaveCount(0)
  await nav(page).getByRole('button', { name: 'Environment', exact: true }).click()
  await expect(dock.locator('.xterm-rows')).toBeVisible({ timeout: 60_000 })
  await dock.locator('.xterm-screen').click()
  // Require new shell output rather than matching the echoed command.
  await page.keyboard.type('printf "\\141ether-dock-resumed\\n"\n')
  await expect(dock.locator('.xterm-rows')).toContainText('aether-dock-resumed', {
    timeout: 30_000,
  })
})
