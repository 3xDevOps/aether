// The update prompts at the smallest window the desktop shell allows, and at
// one smaller than that.
//
// The prompts render above the whole app in a column that cannot scroll
// sideways and, before the shell had a minimum, could be made shorter than
// they are. Two things went wrong there. The strip wrapped, so its controls
// left the top right of the prompt and reappeared at the bottom left under
// the prose - at 1366px wide and narrower, which is most laptops. And once
// the prompts were taller than the window, everything below them, the app
// included, went off the bottom edge.
//
// One `aether gui` per test and no server: the CLI half of `update.check` is
// answered on the member's own machine, so the prompts need nothing else.

import { readFileSync, rmSync } from 'node:fs'
import path from 'node:path'

import { test as base, expect, type Page } from '@playwright/test'

import { type Gateway, startGateway } from './harness/gateway'
import { repoRoot, scratchDir } from './harness/paths'

/**
 * The floor the Electron shell enforces, read from the shell itself so the
 * two cannot drift apart: a window smaller than this is not reachable, and a
 * layout that needs more than this is broken for somebody.
 */
function minimumWindow(): { width: number; height: number } {
  const main = readFileSync(path.join(repoRoot, 'desktop', 'main.js'), 'utf8')
  const read = (key: string): number => {
    const found = [...main.matchAll(new RegExp(`${key}:\\s*(\\d+)`, 'g'))]
    // More than one window would mean this reads the wrong one, and none
    // would mean the shell stopped enforcing a floor at all.
    if (found.length !== 1) {
      throw new Error(`desktop/main.js sets ${found.length} ${key} values, expected 1`)
    }
    return Number(found[0][1])
  }
  return { width: read('minWidth'), height: read('minHeight') }
}

/** update.check with a release out, in the shape internal/localgw answers. */
function available(over: Record<string, unknown> = {}) {
  return {
    cli: {
      version: 'v0.2.0',
      commit: 'abc1234',
      latest: 'v0.3.0',
      update_available: true,
      asset: 'aether-darwin-arm64',
      release_url: 'https://github.com/3xDevOps/Aether/releases/tag/v0.3.0',
      dev: false,
      disabled: false,
      can_self_update: true,
      checked_at: '2026-09-08T10:00:00Z',
    },
    server_version: '',
    server_behind: false,
    supervised: true,
    cli_path: '/usr/local/bin/aether',
    // The longest of the three explanations, so the prose above the button is
    // as tall as this prompt ever gets.
    install_method: 'admin-prompt',
    ...over,
  }
}

const test = base.extend<{ gateway: Gateway }>({
  gateway: async ({}, use, testInfo) => {
    const dir = scratchDir()
    const gateway = await startGateway(dir, 'alice')
    try {
      await use(gateway)
    } finally {
      await gateway.stop()
      // A failed test keeps the gateway's own output; a passing one leaves
      // nothing behind.
      if (testInfo.status === testInfo.expectedStatus) {
        rmSync(dir, { recursive: true, force: true })
      } else {
        await testInfo.attach('aether gui output', { body: gateway.output() })
      }
    }
  },
})

/**
 * Opens the dashboard as the desktop app does. The bridge `desktop/preload.js`
 * exposes makes the SPA draw its own title bar and, because the shell version
 * cannot match the CLI serving it, stack the stale-shell prompt above the CLI
 * one. Two prompts at once is the state the report came from.
 */
async function openDesktop(page: Page, gateway: Gateway): Promise<void> {
  await page.addInitScript(() => {
    ;(window as unknown as { aetherDesktop: unknown }).aetherDesktop = {
      platform: 'linux',
      // No release carries this, so the prompt cannot go missing because the
      // build under test happened to match.
      shellVersion: '0.0.0-stale',
      controls: {
        minimize: () => {},
        toggleMaximize: () => {},
        close: () => {},
        isMaximized: () => Promise.resolve(false),
        onMaximizedChange: () => () => {},
      },
    }
  })
  await page.goto(gateway.url)
  await expect(page.getByText('The desktop app is out of date.')).toBeVisible()
}

/** The opening words of the two prompts a stale desktop shell puts up. */
const prompts = ['The desktop app is out of date.', 'is available.']

/**
 * Both prompts carry their controls on their own first row. This is the
 * regression: while the strip wrapped, the controls sat under the prose
 * wherever the prose was too wide to share the row. Measured on the released
 * layout with the prompt this spec seeds, whose explanation is the longest of
 * the three: the controls share the row only above 1580px, and sit 76px to
 * 160px down at every size below it.
 */
async function expectControlsOnFirstRow(page: Page): Promise<void> {
  // The prompts are found by their own opening words rather than by role
  // alone: other surfaces announce themselves with role="status" too, and a
  // walk that escaped the strip would measure the app instead.
  const rows = await page.evaluate((headlines: string[]) => {
    return headlines.map((headline) => {
      const prompt = [...document.querySelectorAll('[role="status"]')].find((el) =>
        el.textContent?.includes(headline),
      )
      if (!prompt) return null
      // The buttons inside the prose column - a copyable command's own copy
      // control - are part of the prose, not the prompt's controls.
      const prose = prompt.firstElementChild
      const top = prompt.getBoundingClientRect().top
      return [...prompt.querySelectorAll('button, a')]
        .filter((control) => !prose?.contains(control))
        .map((control) => Math.round(control.getBoundingClientRect().top - top))
    })
  }, prompts)
  for (const offsets of rows) {
    expect(offsets).not.toBeNull()
    expect(offsets?.length).toBeGreaterThan(0)
    for (const offset of offsets ?? []) expect(offset).toBeLessThan(40)
  }
}

const { width, height } = minimumWindow()
test.use({ viewport: { width, height } })

test('the update prompt offers its button where the prompt starts', async ({
  page,
  gateway,
}) => {
  await page.route('**/local/v1/update.check', (route) =>
    route.fulfill({ json: available() }),
  )
  // Held open, so the prompt stays in its applying state to be measured.
  let releaseApply = () => {}
  const applyStarted = new Promise<void>((resolve) => {
    releaseApply = resolve
  })
  const held = new Promise<void>((resolve) => page.on('close', () => resolve()))
  await page.route('**/local/v1/update.apply', async (route) => {
    releaseApply()
    await held
    await route.abort()
  })

  await openDesktop(page, gateway)

  const button = page.getByRole('button', { name: 'Update now' })
  await expect(button).toBeInViewport({ ratio: 1 })
  await expect(page.getByRole('button', { name: 'Dismiss' }).first()).toBeInViewport({
    ratio: 1,
  })
  await expectControlsOnFirstRow(page)
  // The prompts must not push the app off the bottom either: the status bar
  // is the last row of the shell, and its right-hand group is the part that
  // used to be squeezed off the edge.
  await expect(page.getByRole('contentinfo')).toBeInViewport({ ratio: 1 })
  await expect(page.getByRole('button', { name: 'Commands' })).toBeInViewport({
    ratio: 1,
  })
  await expect(page.getByRole('button', { name: 'Keyboard shortcuts' })).toBeInViewport({
    ratio: 1,
  })

  await button.click()
  await applyStarted
  await expect(page.getByRole('button', { name: 'Updating...' })).toBeInViewport({
    ratio: 1,
  })
  await expectControlsOnFirstRow(page)
})

test('the update prompt keeps its button in place while the app rebuilds', async ({
  page,
  gateway,
}) => {
  await page.route('**/local/v1/update.check', (route) =>
    route.fulfill({ json: available() }),
  )
  await page.route('**/local/v1/update.apply', (route) =>
    route.fulfill({
      json: {
        updated: ['/usr/local/bin/aether'],
        version: 'v0.3.0',
        restarting: false,
        rebuilding: true,
      },
    }),
  )
  await page.route('**/local/v1/update.status', (route) =>
    route.fulfill({ json: { phase: 'installing dependencies' } }),
  )

  await openDesktop(page, gateway)
  await page.getByRole('button', { name: 'Update now' }).click()
  await expect(page.getByRole('button', { name: 'Rebuilding...' })).toBeInViewport({
    ratio: 1,
  })
  await expectControlsOnFirstRow(page)
})

for (const [state, status, detail] of [
  ['fails', 500, 'writing /usr/local/bin/aether: permission denied'],
  ['is cancelled', 403, 'the administrator dialog was dismissed'],
] as const) {
  test(`the update prompt keeps its button in place when the update ${state}`, async ({
    page,
    gateway,
  }) => {
    await page.route('**/local/v1/update.check', (route) =>
      route.fulfill({ json: available() }),
    )
    await page.route('**/local/v1/update.apply', (route) =>
      route.fulfill({ status, json: { error: { message: detail } } }),
    )

    await openDesktop(page, gateway)
    const button = page.getByRole('button', { name: 'Update now' })
    await button.click()
    // The failure is shown in full, and the button that produced it is still
    // where it was.
    await expect(page.getByText(detail)).toBeVisible()
    await expect(button).toBeInViewport({ ratio: 1 })
    await expectControlsOnFirstRow(page)
  })
}

test('a long build error cannot hide the prompt underneath it', async ({
  page,
  gateway,
}) => {
  // A stress value, not a shape the gateway produces: it records the build's
  // error as one line. The stale-shell prompt prints whatever it gets
  // verbatim, and sits above the prompt carrying Update now, so this is the
  // output that used to push that button out of sight.
  const buildError = Array.from(
    { length: 40 },
    (_, line) => `npm ERR! line ${line}: gyp: build failed with exit status 1`,
  ).join('\n')
  await page.route('**/local/v1/update.check', (route) =>
    route.fulfill({ json: available({ shell_build_error: buildError }) }),
  )

  await openDesktop(page, gateway)
  // The seed really did reach the bound: without this the case cannot tell a
  // clamped error from one that was short enough all along.
  const clamped = await page.evaluate(() => {
    const prompt = [...document.querySelectorAll('[role="status"]')].find((el) =>
      el.textContent?.includes('The desktop app is out of date.'),
    )
    const output = prompt?.querySelector('p.max-h-24')
    return output ? output.scrollHeight > output.clientHeight : false
  })
  expect(clamped).toBe(true)

  // Bounded where it is printed, so however long it runs it cannot scroll the
  // next prompt's controls out of the strip.
  await expect(page.getByRole('button', { name: 'Update now' })).toBeInViewport({
    ratio: 1,
  })
  await expect(page.getByRole('contentinfo')).toBeInViewport({ ratio: 1 })
})

// A browser tab has no minimum: `aether gui` prints a URL and the member can
// make that window any size at all, which is what the desktop shell allowed
// before this ticket. The prompts have to degrade there rather than take the
// window over.
test.describe('a window smaller than the shell allows', () => {
  test.use({ viewport: { width: 800, height: 480 } })

  test('keeps the app on screen and the prompt controls where they belong', async ({
    page,
    gateway,
  }) => {
    await page.route('**/local/v1/update.check', (route) =>
      route.fulfill({ json: available() }),
    )
    await openDesktop(page, gateway)

    // The prompts want more room than this window has, so the strip is in the
    // state the scroll and the floor below it exist for.
    const strip = await page.evaluate(() => {
      const prompt = [...document.querySelectorAll('[role="status"]')].find((el) =>
        el.textContent?.includes('The desktop app is out of date.'),
      )
      const el = prompt?.parentElement
      return { scrollHeight: el?.scrollHeight ?? 0, clientHeight: el?.clientHeight ?? 0 }
    })
    expect(strip.scrollHeight).toBeGreaterThan(strip.clientHeight)

    await expect(page.getByRole('button', { name: 'Update now' })).toBeInViewport({
      ratio: 1,
    })
    // The shell itself survives: the app is not pushed off the bottom.
    await expect(page.getByRole('contentinfo')).toBeInViewport({ ratio: 1 })
    await expectControlsOnFirstRow(page)
  })
})
