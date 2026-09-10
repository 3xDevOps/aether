// Browser-only shell claims that need painted focus or rendered geometry. The
// tests below end on a state that proves the claim is not passing for the
// wrong reason.
//
// Escape: the shell leaves a run-detail route for the board from a `keydown`
// on `window`, and Radix dismisses its dialogs from a capturing document
// listener without stopping propagation, so that same Escape still reaches
// the shell. Ordering is the whole problem, and jsdom does not reproduce it:
// React commits the close in a microtask that runs first, so a guard asking
// "is a dialog open?" finds none and the run is left behind with the dialog.
//
// Focus: every unit test in the suite asserts Tailwind class strings, which
// say a class is on an element and nothing about what is painted. This reads
// the computed outline, and resolves the outline and the background to real
// pixels so a token that lands on the background colour fails here.

import type { Locator, Page } from '@playwright/test'

import { type Aether, expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

/** What the installed `claude` shim runs: the seed repository's own script,
 * from the run checkout the scheduler mounts at /workspace. */
const agentShim = 'sh /workspace/agent.sh'

const task = 'write the result file'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

/**
 * The wizard, up to the moment a run screen is on the page. Neither claim
 * below is about the agent, so nothing here waits for the container: the run
 * screen exists as soon as the launch returns.
 */
async function openFirstRun(page: Page, aether: Aether): Promise<void> {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  await wizard.repository.continue().click()
  aether.installAgent(await memberID(alice), 'claude', agentShim)
  await wizard.agents.skip().click()
  await wizard.expectStep('First run')
  await wizard.firstRun.launch('claude', task)

  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
}

interface Indicator {
  outlineStyle: string
  outlineWidth: number
  /** The outline colour and the colour behind it, as painted RGBA bytes. */
  outline: number[]
  background: number[]
}

/** What the focused element actually shows: the computed outline, and the
 * first ancestor background that is not see-through. */
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

/**
 * Asserts a rendered app focus indicator, not any indicator. Chromium's
 * fallback focus ring is `auto` at 1px in the foreground colour and would
 * satisfy anything looser than this, so deleting the app's indicator would
 * leave the assertion green while proving nothing.
 */
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

  // The shortcut stands down inside the terminal, and the terminal takes the
  // focus when it mounts. Clicking the title is how a reader gets out of it.
  await page.getByRole('heading', { name: task, exact: true }).click()
  await page.keyboard.press('?')

  const dialog = page.getByRole('dialog', { name: 'Keyboard shortcuts' })
  await expect(dialog).toBeVisible()

  await page.keyboard.press('Escape')

  await expect(dialog).toHaveCount(0)
  // Still on the run the dialog was opened from: the strip and the title only
  // exist on a run-detail route, and the Terminal tab is the one that was
  // open before the dialog.
  const tabs = page.getByRole('tablist', { name: 'Run tabs' })
  await expect(tabs).toBeVisible()
  await expect(tabs.getByRole('tab', { name: 'Terminal' })).toHaveAttribute(
    'aria-selected',
    'true',
  )
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()

  // The same key with nothing over the run does leave it. Without this the
  // assertions above would also pass on a build where the shortcut never
  // registered, which is not what they are meant to prove.
  await page.keyboard.press('Escape')
  await expect(page.getByRole('heading', { name: 'Board', exact: true })).toBeVisible()
  await expect(tabs).toHaveCount(0)
})

test('keyboard focus paints a visible outline on the shell controls', async ({
  page,
  aether,
}) => {
  await openFirstRun(page, aether)

  // `:focus-visible` follows the last input the browser saw, and the wizard
  // above is all mouse clicks: an `element.focus()` from script after one of
  // those does not match in Chromium. Every focus below therefore ends on a
  // real key press - an arrow along the run strip, which is the only way its
  // roving tabindex moves focus at all, and a Tab into the next surface.
  const tabs = page.getByRole('tablist', { name: 'Run tabs' })
  await tabs.getByRole('tab', { name: 'Terminal' }).focus()
  await page.keyboard.press('ArrowLeft')
  const overview = tabs.getByRole('tab', { name: 'Overview' })
  await expect(overview).toBeFocused()
  expectVisibleFocus('the run tab', await indicator(overview))

  // A row that fills a scroll container must keep its focus outline visible
  // at its edges; the painted check allows either inset or outset outlines.
  await page.keyboard.press('Escape')
  const row = page
    .getByRole('complementary', { name: 'Runs' })
    .getByRole('button', { name: new RegExp(task) })
  await row.focus()
  await page.keyboard.press('Shift+Tab')
  await page.keyboard.press('Tab')
  await expect(row).toBeFocused()
  expectVisibleFocus('the sidebar run row', await indicator(row))

  const surfaces = page.getByRole('navigation', { name: 'Surfaces' })
  const board = surfaces.getByRole('button', { name: 'Board', exact: true })
  await board.focus()
  await page.keyboard.press('Tab')
  const allRuns = surfaces.getByRole('button', { name: 'All runs', exact: true })
  await expect(allRuns).toBeFocused()
  expectVisibleFocus('the sidebar surface button', await indicator(allRuns))

  // The neighbouring control, reached by mouse instead: no outline. Without
  // this the checks above would also pass on a control that is outlined all
  // the time, which is a different bug and not the one they are proving.
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

  const sidebar = page.getByRole('complementary', { name: 'Runs' })
  const rail = page.getByRole('navigation', { name: 'Surfaces' })
  const separator = page.getByRole('separator', { name: 'Resize sidebar' })
  const before = await sidebar.boundingBox()
  const beforeRail = await rail.boundingBox()
  const handle = await separator.boundingBox()
  if (!before || !beforeRail || !handle) {
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
  const afterRail = await rail.boundingBox()
  if (!after || !afterRail) throw new Error('sidebar disappeared after resize')
  expect(after.x).toBe(before.x)
  expect(afterRail.width).toBe(beforeRail.width)

  await separator.focus()
  await page.keyboard.press('Home')
  await expect(separator).toHaveAttribute('aria-valuenow', '200')
  await expect
    .poll(async () => (await sidebar.boundingBox())?.width ?? 0)
    .toBe(200)

  const runs = sidebar.getByText('Runs', { exact: true })
  const toolbar = runs.locator('..')
  const firstGroup = sidebar.getByRole('heading').first()
  const groupBy = sidebar.getByRole('group', { name: 'Group runs by' })
  const member = groupBy.getByRole('button', { name: 'Member', exact: true })
  const launch = sidebar.getByRole('button', { name: 'New run', exact: true })
  await expect(member).toBeVisible()
  await expect(launch).toBeVisible()

  const toolbarBox = await toolbar.boundingBox()
  const firstGroupBox = await firstGroup.boundingBox()
  const groupBox = await groupBy.boundingBox()
  const memberBox = await member.boundingBox()
  const launchBox = await launch.boundingBox()
  const minimum = await sidebar.boundingBox()
  if (
    !toolbarBox ||
    !firstGroupBox ||
    !groupBox ||
    !memberBox ||
    !launchBox ||
    !minimum
  ) {
    throw new Error('minimum-width sidebar controls did not render')
  }

  expect(toolbarBox.height).toBeGreaterThan(35)
  expect(toolbarBox.y + toolbarBox.height).toBeLessThanOrEqual(firstGroupBox.y)
  for (const control of [groupBox, memberBox, launchBox]) {
    expect(control.x).toBeGreaterThanOrEqual(minimum.x)
    expect(control.x + control.width).toBeLessThanOrEqual(minimum.x + minimum.width)
  }
})
