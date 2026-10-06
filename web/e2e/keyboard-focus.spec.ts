// Escape: Radix dismisses dialogs from a capturing document listener without
// stopping propagation, so the same Escape reaches the shell's window keydown.
// jsdom does not reproduce that ordering.
//
// Focus: unit tests only assert class strings; this resolves the computed
// outline and background to painted pixels.

import type { Locator, Page } from '@playwright/test'

import { type Aether, expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

/** The seed repository's own script, from the run checkout mounted at /workspace. */
const agentShim = 'sh /workspace/agent.sh'

const task = 'write the result file'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

async function openFirstRun(page: Page, aether: Aether): Promise<void> {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.connect.link(aether.server.addr, { name: 'Alice' })
  await wizard.connect.continue().click()
  await wizard.repository.createFromClone('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  await wizard.repository.continue().click()
  aether.installAgent(await memberID(alice), 'claude', agentShim)
  await wizard.agent.skip().click()
  await wizard.expectStep('First run')
  await wizard.firstRun.launch('claude', task)

  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  // The fake agent exits, but the interactive run keeps its supervised shell.
  await expect(page.locator('.xterm-rows:not([data-aether-frozen-view] *)')).toContainText('agent-ready')
  await expect(page.locator('header').filter({ hasText: task })).toContainText('Working')
}

async function closeFirstRun(page: Page): Promise<void> {
  const header = page.locator('header').filter({ hasText: task })
  await expect(header).toContainText('Working')
  await header.getByRole('button', { name: 'More', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Close run…', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Close this run?' })
  await expect(dialog).toBeVisible()
  await dialog.getByRole('button', { name: 'Merged', exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await expect(header).toContainText('Done')
  await expect(header).toContainText('closed; retained container')
  await expect(dialog).toHaveCount(0)
}

interface Indicator {
  outlineStyle: string
  outlineWidth: number
  /** Painted RGBA bytes. */
  outline: number[]
  background: number[]
}

async function indicator(el: Locator): Promise<Indicator> {
  return el.evaluate((node: HTMLElement) => {
    const paint = (color: string): number[] => {
      const canvas = document.createElement('canvas')
      canvas.width = canvas.height = 1
      const ctx = canvas.getContext('2d')!
      ctx.clearRect(0, 0, 1, 1)
      ctx.fillStyle = color
      ctx.fillRect(0, 0, 1, 1)
      return Array.from(ctx.getImageData(0, 0, 1, 1).data)
    }

    let behind: HTMLElement | null = node
    let background = [0, 0, 0, 0]
    while (behind && background[3] === 0) {
      background = paint(getComputedStyle(behind).backgroundColor)
      behind = behind.parentElement
    }

    const style = getComputedStyle(node)
    return {
      outlineStyle: style.outlineStyle,
      outlineWidth: Number.parseFloat(style.outlineWidth),
      outline: paint(style.outlineColor),
      background,
    }
  })
}

/** WCAG relative luminance, from painted sRGB bytes. */
function luminance([r, g, b]: number[]): number {
  const channel = (v: number) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

function contrast(a: number[], b: number[]): number {
  const [light, dark] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (light + 0.05) / (dark + 0.05)
}

// Chromium's fallback focus ring is `auto` at 1px, so anything looser than
// this stays green with the app's indicator deleted.
function expectVisibleFocus(what: string, seen: Indicator): void {
  expect(seen.outlineStyle, `${what}: outline-style`).toBe('solid')
  expect(seen.outlineWidth, `${what}: outline-width`).toBeGreaterThanOrEqual(2)
  // WCAG 1.4.11 asks 3:1 of a focus indicator against what it sits on.
  expect(
    contrast(seen.outline, seen.background),
    `${what}: outline contrast`,
  ).toBeGreaterThanOrEqual(3)
}

test('Escape closes a dialog on a run without leaving the run', async ({
  page,
  aether,
}) => {
  await openFirstRun(page, aether)

  // The shortcut stands down inside the terminal, which takes focus on mount.
  await page.getByRole('heading', { name: task, exact: true }).click()
  await page.keyboard.press('?')

  const dialog = page.getByRole('dialog', { name: 'Keyboard shortcuts' })
  await expect(dialog).toBeVisible()

  await page.keyboard.press('Escape')

  await expect(dialog).toHaveCount(0)
  const tabs = page.getByRole('tablist', { name: 'Run tabs' })
  await expect(tabs).toBeVisible()
  await expect(tabs.getByRole('tab', { name: 'Terminal' })).toHaveAttribute(
    'aria-selected',
    'true',
  )
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()

  await closeFirstRun(page)

  // Proves the shell shortcut registered at all.
  await page.keyboard.press('Escape')
  await expect(page.getByRole('heading', { name: 'Board', exact: true })).toBeVisible()
  await expect(tabs).toHaveCount(0)
})

test('Escape closes the footer menu without leaving the run', async ({
  page,
  aether,
}) => {
  await openFirstRun(page, aether)

  // The shortcut stands down inside the terminal, which takes focus on mount.
  await page.getByRole('heading', { name: task, exact: true }).click()

  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: /, Live$/ }).click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()

  await page.keyboard.press('Escape')

  await expect(menu).toHaveCount(0)
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()

  await closeFirstRun(page)

  // Proves the shell shortcut registered at all.
  await page.keyboard.press('Escape')
  await expect(page.getByRole('heading', { name: 'Board', exact: true })).toBeVisible()
})

test('keyboard focus paints a visible outline on the shell controls', async ({
  page,
  aether,
}) => {
  await openFirstRun(page, aether)

  // `:focus-visible` follows the last input, and the wizard used the mouse, so
  // a scripted focus() does not match in Chromium. Every focus ends on a key press.
  const tabs = page.getByRole('tablist', { name: 'Run tabs' })
  await tabs.getByRole('tab', { name: 'Terminal' }).focus()
  await page.keyboard.press('ArrowLeft')
  const events = tabs.getByRole('tab', { name: 'Events' })
  await expect(events).toBeFocused()
  expectVisibleFocus('the run tab', await indicator(events))
  // Closing the run first would move its row under the collapsed Finished group.
  const row = page
    .getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' })
    .getByRole('button', { name: new RegExp(task) })
  await row.focus()
  await page.keyboard.press('Shift+Tab')
  await page.keyboard.press('Tab')
  await expect(row).toBeFocused()
  expectVisibleFocus('the sidebar run row', await indicator(row))

  const surfaces = page.getByRole('navigation', { name: 'Aether' })
  const board = surfaces.getByRole('button', { name: 'Board', exact: true })
  await board.focus()
  await page.keyboard.press('Tab')
  const missions = surfaces.getByRole('button', { name: 'Swarms', exact: true })
  await expect(missions).toBeFocused()
  expectVisibleFocus('the Swarms sidebar row', await indicator(missions))

  await closeFirstRun(page)
  await page.keyboard.press('Escape')

  // Mouse focus must not outline, or the checks above pass on an always-outlined control.
  await board.click()
  await expect(board).toBeFocused()
  expect((await indicator(board)).outlineStyle).toBe('none')
})

test('resizing the sidebar follows the pointer delta and keeps minimum controls reachable', async ({
  page,
  aether,
}) => {
  await page.setViewportSize({ width: 1280, height: 720 })
  await openFirstRun(page, aether)

  const sidebar = page.locator('#sidebar')
  const separator = page.getByRole('separator', { name: 'Resize sidebar' })
  const before = await sidebar.boundingBox()
  const handle = await separator.boundingBox()
  if (!before || !handle) {
    throw new Error('sidebar splitter did not render')
  }

  const startX = handle.x + handle.width / 2
  const startY = handle.y + handle.height / 2
  await page.mouse.move(startX, startY)
  await page.mouse.down()
  await page.mouse.move(startX + 1, startY)
  await expect
    .poll(async () => (await sidebar.boundingBox())?.width ?? 0)
    .toBe(before.width + 1)
  await page.mouse.up()

  const after = await sidebar.boundingBox()
  if (!after) throw new Error('sidebar disappeared after resize')
  expect(after.x).toBe(before.x)

  await separator.focus()
  await page.keyboard.press('Home')
  const minimumWidth = Number(await separator.getAttribute('aria-valuemin'))
  await expect(separator).toHaveAttribute('aria-valuenow', String(minimumWidth))
  await expect
    .poll(async () => (await sidebar.boundingBox())?.width ?? 0)
    .toBe(minimumWidth)

  const minimum = await sidebar.boundingBox()
  if (!minimum) throw new Error('minimum-width sidebar did not render')
  for (const control of [
    sidebar.getByRole('button', { name: 'Search', exact: true }),
    sidebar.getByRole('button', { name: 'New run', exact: true }),
    sidebar.getByRole('button', { name: 'Mine', exact: true }),
  ]) {
    const box = await control.boundingBox()
    if (!box) throw new Error('a minimum-width sidebar control did not render')
    expect(box.x).toBeGreaterThanOrEqual(minimum.x)
    expect(box.x + box.width).toBeLessThanOrEqual(minimum.x + minimum.width)
  }
  await closeFirstRun(page)
})
