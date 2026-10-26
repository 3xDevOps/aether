// Fix this on a failed run through the assistant's approval card to the run
// it launched, at a desktop size. assistant-script.ts says what is scripted.

import { fixThisJourney } from './assistant-script'
import { test } from './fixtures'
import { dockerReachable } from './harness/server'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('Fix this launches a run from the approval card and opens it from the action card', async ({ page, aether }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await fixThisJourney(page, aether, testInfo, 'fix-this-desktop')
})
