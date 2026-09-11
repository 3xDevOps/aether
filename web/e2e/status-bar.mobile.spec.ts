// The status bar on a phone. The readouts that do not fit are behind one
// popup the member taps open, and every control it carries has to stay
// inside the viewport - including the theme toggle on the bottom edge, which
// a tap must land on without the document growing under the finger.
//
// The state that makes the bar too wide is a server that has gone away: the
// notice explaining it is the longest thing the bar ever carries, and it
// needs a real server to leave. `status-bar-sizing.spec.ts` owns the same
// bar at desktop widths.

import { expect, test } from './mobile'
import { OnboardingWizard } from './pages/wizard'

/** The controls the bar always offers, wherever the layout puts them. */
const controls = ['Commands', 'Keyboard shortcuts', 'Theme: system']
const unreachableNotice =
  'server unreachable over SSH - check the server and network; retrying'
// Long enough to overflow the bar's left group, which is what took the
// controls off the edge before the readouts learned to give way.
const longName =
  'Alexandria Montgomery Workbench Collaboration and Infrastructure Verification Member'

test('the phone status bar keeps every control inside the viewport', async ({
  page,
  aether,
}) => {
  const viewport = page.viewportSize()
  if (!viewport) throw new Error('the mobile project always sets a viewport')

  // The long name belongs to a member who joined on an invite: a fresh
  // server's first identity is named by the key it linked with.
  const alice = await aether.member('alice')
  const adminWizard = await OnboardingWizard.open(page, alice.url)
  await adminWizard.link.link(aether.server.addr)
  await adminWizard.link.continue().tap()

  const code = await aether.invite(alice)
  const collaborator = await aether.member('alexandria')
  const wizard = await OnboardingWizard.open(page, collaborator.url)
  await wizard.link.link(aether.server.addr, { invite: code, name: longName })
  await wizard.link.continue().tap()

  // Linking is what fills the bar: the version label, the member name and
  // the disk gauge all come from a server that answered.
  const footer = page.getByRole('contentinfo')
  await expect(footer).toContainText('aether ')

  const trigger = footer.getByRole('button', { name: 'Show status details' })
  await expect(trigger).toHaveAttribute('aria-expanded', 'false')
  await trigger.tap()

  // TeamStatus starts its own disk read after hydration. Wait for the gauge
  // before taking the server away; the version label only proves
  // `server.info` answered.
  const disk = footer.locator('[aria-label="Disk usage"]')
  await expect(disk).toBeVisible()

  await aether.server.stop()

  const notice = footer.getByRole('status', { name: unreachableNotice })
  await expect(notice).toHaveText(unreachableNotice)
  await expect(footer.getByText(longName, { exact: true })).toBeVisible()
  for (const name of controls) {
    await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
  }

  const popup = footer.locator('#status-details > div')
  const popupBox = await popup.boundingBox()
  if (!popupBox) throw new Error('the status details popup did not render')
  expect(popupBox.x).toBeGreaterThanOrEqual(0)
  expect(popupBox.y).toBeGreaterThanOrEqual(0)
  expect(popupBox.x + popupBox.width).toBeLessThanOrEqual(viewport.width)
  expect(popupBox.y + popupBox.height).toBeLessThanOrEqual(viewport.height)
  await expect(popup).toHaveCSS('overflow-y', 'auto')

  // Nothing the popup carries may push the page sideways or downwards: the
  // shell owns the whole screen and the member has no window to widen.
  const overflow = await page.evaluate(() => ({
    horizontal: Math.max(
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
      document.body.scrollWidth - document.body.clientWidth,
    ),
    vertical: Math.max(
      document.documentElement.scrollHeight - document.documentElement.clientHeight,
      document.body.scrollHeight - document.body.clientHeight,
    ),
  }))
  expect(overflow).toEqual({ horizontal: 0, vertical: 0 })

  // The same tap closes it again, and the theme toggle beside it answers a
  // finger on the bottom edge of the screen.
  await trigger.tap()
  await expect(notice).toBeHidden()
  await page.getByRole('button', { name: 'Theme: system' }).tap()
  await expect(page.getByRole('button', { name: 'Theme: light' })).toBeVisible()
})
