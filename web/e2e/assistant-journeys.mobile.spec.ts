// The phone half of assistant-journeys.spec.ts: the same journey in the
// assistant sheet, whose Open run closes the sheet onto the run.

import { fixThisJourney } from './assistant-script'
import { dockerReachable } from './harness/server'
import { test } from './mobile'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('Fix this launches a run from the approval card and opens it from the action card on a phone', async ({ page, aether }, testInfo) => {
  await fixThisJourney(page, aether, testInfo, 'fix-this-mobile')
})
