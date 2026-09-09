// The status bar at the smallest window the desktop shell allows, and at one
// smaller than that. The bar is a single row and nothing in the shell scrolls
// sideways, so a left group that outgrows the window pushes the palette,
// shortcuts and theme controls past the right edge, where nothing can reach
// them.
//
// The state that makes the left group too wide is a server that has gone
// away: the notice explaining it is the longest thing the bar ever carries,
// and it needs a real server to leave. Everything here is real - the member
// links, the server is stopped, and the gateway reports what it finds.

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

  await aether.server.stop()
  await expect(page.getByText('server unreachable over SSH')).toBeVisible()

  for (const size of sizes) {
    await page.setViewportSize(size)
    for (const name of controls) {
      await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
    }
    // The readouts give way inside their own group rather than pushing it:
    // an overflowing left group is what took the controls off the edge.
    const overflow = await page.evaluate(() => {
      const left = document.querySelector('footer')?.firstElementChild
      return left ? left.scrollWidth - left.clientWidth : -1
    })
    expect(overflow).toBe(0)
  }
})
