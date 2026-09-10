// The status bar at the smallest window the desktop shell allows, and below
// that floor in a browser tab. At compact widths the connection, status-slot
// actions and theme remain on screen while the secondary readouts use a native
// details popup. The wide row expands those readouts in place.
//
// The state that makes the left group too wide is a server that has gone
// away: the notice explaining it is the longest thing the bar ever carries,
// and it needs a real server to leave. Everything here is real - the member
// links, the server is stopped, and the gateway reports what it finds.
//
// Keep the compact disclosure keyboard reachable: a mouse-only check would
// miss the native details behavior that makes the offline notice available.
//
// The mobile case also keeps the status-slot actions inside the bounded popup,
// where they may wrap without widening the page.

import { expect, test } from './fixtures'
import { OnboardingWizard } from './pages/wizard'

/** The right-hand group, which never gives way and so must always fit. */
const controls = ['Commands', 'Keyboard shortcuts', 'Theme: system']

const sizes = [
  // The floor desktop/main.js enforces.
  { width: 960, height: 600 },
  // A browser tab, which has no floor at all.
  { width: 800, height: 480 },
]
const wideSize = { width: 1280, height: 600 }
const mobileSize = { width: 390, height: 480 }
const unreachableNotice =
  'server unreachable over SSH - check the server and network; retrying'

test('the status bar keeps its controls on screen with every readout up', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr)
  await wizard.link.continue().click()
  // Linking is what fills the bar: the version label, the member name and the
  // disk gauge all come from a server that answered. The version itself is
  // whatever `git describe` made of this checkout - a bare commit on a clone
  // with no tags - so only the label around it is asserted.
  await expect(page.getByRole('contentinfo')).toContainText('aether ')
  const footer = page.getByRole('contentinfo')

  await aether.server.stop()

  for (const size of sizes) {
    await page.setViewportSize(size)
    for (const name of controls) {
      await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
    }
    const summary = footer.locator('summary[aria-label="Show status details"]')
    const details = footer.locator('details')
    const notice = footer.getByRole('status', { name: unreachableNotice })
    await expect(summary).toBeVisible()
    if (await details.evaluate((element) => (element as HTMLDetailsElement).open)) {
      await summary.focus()
      await summary.press('Enter')
      await expect(notice).toBeHidden()
    }
    await summary.focus()
    await summary.press('Enter')
    await expect(notice).toBeVisible()
    await expect(notice).toHaveText(unreachableNotice)

    // The readouts give way inside their own group rather than pushing it:
    // an overflowing left group is what took the controls off the edge.
    const overflow = await page.evaluate(() => {
      const left = document.querySelector('footer')?.firstElementChild
      return left ? left.scrollWidth - left.clientWidth : -1
    })
    expect(overflow).toBe(0)
  }

  await page.setViewportSize(wideSize)
  await expect(footer.locator('summary[aria-label="Show status details"]')).toBeHidden()
  await expect(footer.getByRole('status', { name: unreachableNotice })).toBeVisible()
  for (const name of controls) {
    await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
  }

  await page.setViewportSize(mobileSize)
  const mobileSummary = footer.locator('summary[aria-label="Show status details"]')
  const mobileDetails = footer.locator('details')
  const mobileNotice = footer.getByRole('status', { name: unreachableNotice })
  await expect(mobileSummary).toBeVisible()
  if (await mobileDetails.evaluate((element) => (element as HTMLDetailsElement).open)) {
    await mobileSummary.focus()
    await mobileSummary.press('Enter')
    await expect(mobileNotice).toBeHidden()
  }
  await mobileSummary.focus()
  await mobileSummary.press('Enter')
  await expect(mobileNotice).toBeVisible()
  await expect(mobileNotice).toHaveText(unreachableNotice)
  for (const name of controls) {
    await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
  }
  const mobileOverflow = await page.evaluate(() =>
    Math.max(
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
      document.body.scrollWidth - document.body.clientWidth,
    ),
  )
  expect(mobileOverflow).toBe(0)
})
