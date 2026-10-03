// A real server shutdown exercises complete diagnostics and touch scrolling
// in the phone's bounded status popup.

import { expect, test } from './mobile'
import { OnboardingWizard } from './pages/wizard'

/** The controls the bar always offers, wherever the layout puts them. */
const controls = ['Search runs and commands', 'Keyboard shortcuts', 'Show status details']
const unreachableNotice =
  'server unreachable over SSH - check the server and network; retrying'
// Long enough to overflow the bar's left group, which is what took the
// controls off the edge before the readouts learned to give way.
const longName =
  'Alexandria Montgomery Workbench Collaboration and Infrastructure Verification Member'
/**
 * The same phone with almost no height left. Nothing a phone puts in the
 * status bar makes the readouts taller than the popup's own 70vh ceiling,
 * so a screen this short is the only way to reach that ceiling - and
 * reaching it is the point: what the popup keeps past its edge has to stay
 * scrollable rather than be cut off.
 */
const shortScreen = { width: 412, height: 200 }

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

  // On a short screen the readouts stop fitting, which is where a bounded
  // popup has to prove itself: it stays inside the screen, and everything
  // it pushes past its own edge is still reachable by scrolling to it.
  await page.setViewportSize(shortScreen)
  await expect(notice).toBeVisible()
  const shortBox = await popup.boundingBox()
  if (!shortBox) throw new Error('the status details popup did not render')
  expect(shortBox.y).toBeGreaterThanOrEqual(0)
  expect(shortBox.y + shortBox.height).toBeLessThanOrEqual(shortScreen.height)

  const below = await popup.evaluate((element) => element.scrollHeight - element.clientHeight)
  expect(below).toBeGreaterThan(0)
  const usage = footer.getByRole('button', { name: 'Usage', exact: true })
  await expect(usage).not.toBeInViewport()
  const input = await page.context().newCDPSession(page)
  // Dispatch real, frame-paced touch moves inside the resized popup. Chromium's
  // synthesized touch scroll can deliver only touchstart/touchend under mobile
  // emulation, leaving native scrolling unexercised.
  const distance = shortBox.height - 32
  try {
    for (let swipe = 0; swipe < Math.ceil(below / (distance / 2)); swipe++) {
      const x = shortBox.x + shortBox.width / 2
      const y = shortBox.y + shortBox.height - 16
      await input.send('Input.dispatchTouchEvent', {
        type: 'touchStart', touchPoints: [{ x, y, id: 1 }],
      })
      for (let step = 1; step <= 10; step++) {
        await input.send('Input.dispatchTouchEvent', {
          type: 'touchMove', touchPoints: [{ x, y: y - distance * step / 10, id: 1 }],
        })
        await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())))
      }
      await input.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    }
  } finally {
    await input.detach()
  }
  await expect(usage).toBeInViewport({ ratio: 1 })
  for (const name of controls) {
    await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
  }
  const shortOverflow = await page.evaluate(() =>
    Math.max(
      document.documentElement.scrollHeight - document.documentElement.clientHeight,
      document.body.scrollHeight - document.body.clientHeight,
    ),
  )
  expect(shortOverflow).toBe(0)

  // Closing details leaves the global keyboard help independently reachable.
  await page.setViewportSize(viewport)
  await trigger.tap()
  await expect(notice).toBeHidden()
  await page.getByRole('button', { name: 'Keyboard shortcuts' }).tap()
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
})
