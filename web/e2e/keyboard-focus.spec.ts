// Two accessibility claims only a real browser can settle. Both tests end on
// a control that proves the claim is not passing for the wrong reason.
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
  outlineOffset: number
  /** The outline colour, the colour behind it and the app's own `--ring`, as
   * painted RGBA bytes. Every one is an `oklch()` string that getComputedStyle
   * hands back verbatim, so comparing the strings would call two identical
   * colours different. */
  outline: number[]
  background: number[]
  ring: number[]
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
      outlineOffset: Number.parseFloat(style.outlineOffset),
      outline: paint(style.outlineColor),
      background,
      ring: paint(
        getComputedStyle(document.documentElement).getPropertyValue('--ring'),
      ),
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
 * Asserts the app's own indicator, not any indicator. Chromium's fallback
 * focus ring is `auto` at 1px in the foreground colour and would satisfy
 * anything looser than this, so deleting the token would leave the assertion
 * green while proving nothing.
 */
function expectVisibleFocus(what: string, seen: Indicator, offset: number): void {
  expect(seen.outlineStyle, `${what}: outline-style`).toBe('solid')
  expect(seen.outlineWidth, `${what}: outline-width`).toBeGreaterThanOrEqual(2)
  expect(seen.outlineOffset, `${what}: outline-offset`).toBe(offset)
  expect(seen.outline, `${what}: outline colour is --ring`).toEqual(seen.ring)
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
  expectVisibleFocus('the run tab', await indicator(overview), 2)

  // A row that fills a scroll container draws the same outline inside, which
  // is the one claim the class assertions in a11y.test.tsx cannot check: the
  // negative offset only wins if it carries the same variant as the token.
  await page.keyboard.press('Escape')
  const row = page
    .getByRole('complementary', { name: 'Runs' })
    .getByRole('button', { name: new RegExp(task) })
  await row.focus()
  await page.keyboard.press('Shift+Tab')
  await page.keyboard.press('Tab')
  await expect(row).toBeFocused()
  expectVisibleFocus('the sidebar run row', await indicator(row), -2)

  const surfaces = page.getByRole('navigation', { name: 'Surfaces' })
  const board = surfaces.getByRole('button', { name: 'Board', exact: true })
  await board.focus()
  await page.keyboard.press('Tab')
  const allRuns = surfaces.getByRole('button', { name: 'All runs', exact: true })
  await expect(allRuns).toBeFocused()
  expectVisibleFocus('the sidebar surface button', await indicator(allRuns), 2)

  // The neighbouring control, reached by mouse instead: no outline. Without
  // this the checks above would also pass on a control that is outlined all
  // the time, which is a different bug and not the one they are proving.
  await board.click()
  await expect(board).toBeFocused()
  expect((await indicator(board)).outlineStyle).toBe('none')
})
