// The phone half of the dashboard suite: the same real stack `fixtures.ts`
// builds, driven through the `mobile` Playwright project, which emulates a
// Pixel 7 - touch input, a 412px viewport and a mobile user agent.
//
// Specs that import `test` from here must be named `*.mobile.spec.ts`; that
// is the pattern the mobile project matches and the desktop project skips.

import type { Page } from '@playwright/test'

import { test as base } from './fixtures'

/**
 * What a soft keyboard takes from a portrait phone. Chrome on Android leaves
 * roughly 300-340 CSS pixels of page below its own keyboard.
 */
const softKeyboardHeight = 320

/**
 * Raises a simulated soft keyboard and returns the call that lowers it.
 *
 * Playwright cannot raise the platform keyboard, so this shrinks the layout
 * viewport instead - the shape a keyboard takes on a page that asks for
 * `interactive-widget=resizes-content`. It is the harsher of the two shapes:
 * a browser that only shrinks the visual viewport still has the full page
 * laid out behind the keyboard.
 */
export async function raiseSoftKeyboard(
  page: Page,
): Promise<() => Promise<void>> {
  const viewport = page.viewportSize()
  if (!viewport) throw new Error('the mobile project always sets a viewport')
  await page.setViewportSize({
    width: viewport.width,
    height: viewport.height - softKeyboardHeight,
  })
  return () => page.setViewportSize(viewport)
}

export const test = base.extend<{ phoneLayout: void }>({
  // Every mobile spec ends with a picture of the phone it drove: DOM
  // assertions pass on a layout no one would want to use, and the report
  // otherwise keeps nothing from a green run.
  //
  // Naming `aether` is what puts this teardown before the server's: fixtures
  // tear down in reverse order of setup, and a screenshot taken after the
  // stack is gone shows a disconnected shell.
  phoneLayout: [
    async ({ page, aether: _aether }, use, testInfo) => {
      await use()
      await testInfo.attach('phone layout', {
        body: await page.screenshot({ fullPage: true }),
        contentType: 'image/png',
      })
    },
    { auto: true },
  ],
})

export { expect } from '@playwright/test'
