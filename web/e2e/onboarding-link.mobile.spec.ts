// Linking from a phone on what a soft keyboard leaves of the screen.
//
// A keyboard takes most of a phone's screen, and what is left has to hold
// the field being typed into and the button that submits it.
// `raiseSoftKeyboard` shrinks the layout viewport to that height; it is a
// short-viewport proxy rather than a real keyboard - see e2e/mobile.ts.

import { expect, raiseSoftKeyboard, test } from './mobile'
import { OnboardingWizard } from './pages/wizard'

test('the Link step stays usable at the height a keyboard leaves', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const wizard = await OnboardingWizard.open(page, alice.url)

  const address = wizard.link.section.getByLabel('Server address')
  await address.tap()
  const lowerKeyboard = await raiseSoftKeyboard(page)

  await expect(address).toBeFocused()
  await expect(address).toBeInViewport({ ratio: 1 })
  await page.keyboard.type(aether.server.addr)
  await expect(address).toHaveValue(aether.server.addr)

  // The shell owns the screen, so the short viewport must not turn the page
  // itself into a scroller: the step scrolls inside its own pane.
  const grew = await page.evaluate(
    () =>
      document.documentElement.scrollHeight -
      document.documentElement.clientHeight,
  )
  expect(grew).toBe(0)

  // The submit is the other half. The wizard's header and step list fill a
  // phone screen on their own, so it starts below the fold: what the short
  // viewport may not do is keep it from being scrolled into reach.
  const link = wizard.link.button('Link')
  await link.scrollIntoViewIfNeeded()
  await expect(link).toBeInViewport({ ratio: 1 })
  await link.tap()
  await expect(wizard.link.section).toContainText('(admin)')

  await lowerKeyboard()
  await wizard.link.continue().tap()
  await wizard.expectStep('Git identity')
})
