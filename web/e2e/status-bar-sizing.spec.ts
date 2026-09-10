// The status bar at the smallest window the desktop shell allows, and below
// that floor in a browser tab. At compact widths the connection, status-slot
// actions and theme remain on screen while the secondary readouts use a
// collapsible popup. The wide row expands those readouts in place.
//
// The state that makes the left group too wide is a server that has gone
// away: the notice explaining it is the longest thing the bar ever carries,
// and it needs a real server to leave. Everything here is real - the member
// links, the server is stopped, and the gateway reports what it finds.
//
// Keep the compact disclosure keyboard reachable: a mouse-only check would
// miss the disclosure behavior that makes the offline notice available.
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
  const adminWizard = await OnboardingWizard.open(page, alice.url)
  await adminWizard.link.link(aether.server.addr)
  await adminWizard.link.continue().click()

  const code = await aether.invite(alice)
  const collaborator = await aether.member('alexandria')
  const longName =
    'Alexandria Montgomery Workbench Collaboration and Infrastructure Verification Member'
  const wizard = await OnboardingWizard.open(page, collaborator.url)
  await wizard.link.link(aether.server.addr, { invite: code, name: longName })
  await expect(wizard.link.section).toContainText('(collaborator)')
  await wizard.link.continue().click()

  // Linking is what fills the bar: the version label, the member name and the
  // disk gauge all come from a server that answered. The version itself is
  // whatever `git describe` made of this checkout - a bare commit on a clone
  // with no tags - so only the label around it is asserted.
  await expect(page.getByRole('contentinfo')).toContainText('aether ')
  const footer = page.getByRole('contentinfo')

  // TeamStatus starts its independent disk read after hydration. Wait for the
  // actual gauge at the wide layout before taking the server away; the version
  // label above only proves server.info answered.
  await page.setViewportSize(wideSize)
  await expect(footer.locator('[aria-label="Disk usage"]')).toBeVisible()

  await aether.server.stop()

  for (const size of sizes) {
    await page.setViewportSize(size)
    for (const name of controls) {
      await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
    }
    const trigger = footer.getByRole('button', { name: 'Show status details' })
    const notice = footer.getByRole('status', { name: unreachableNotice })
    await expect(trigger).toBeVisible()
    if ((await trigger.getAttribute('aria-expanded')) === 'true') {
      await trigger.focus()
      await trigger.press('Enter')
      await expect(notice).toBeHidden()
    }
    await trigger.focus()
    await trigger.press('Enter')
    await expect(notice).toBeVisible()
    await expect(notice).toHaveText(unreachableNotice)
    const localStatus = footer.getByRole('button', { name: 'Not linked', exact: true })
    const memberRow = footer.getByText(longName, { exact: true })
    const diskRow = footer.locator('[aria-label="Disk usage"]')
    await expect(localStatus).toBeVisible()
    await expect(memberRow).toBeVisible()
    await expect(diskRow).toBeVisible()
    const localBox = await localStatus.boundingBox()
    const memberBox = await memberRow.boundingBox()
    if (!localBox || !memberBox) throw new Error('compact status rows did not render')
    expect(localBox.y + localBox.height).toBeLessThanOrEqual(memberBox.y)
    const compactRows = await memberRow.evaluate((element) => {
      const memberElement = element as HTMLElement
      const member = memberElement.getBoundingClientRect()
      const disk = document.querySelector<HTMLElement>('[aria-label="Disk usage"]')
      const diskRect = disk?.getBoundingClientRect()
      return {
        memberBottom: member.bottom,
        memberHeight: member.height,
        memberClientHeight: memberElement.clientHeight,
        memberScrollHeight: memberElement.scrollHeight,
        diskTop: diskRect?.top ?? -1,
      }
    })
    expect(compactRows.memberHeight).toBeGreaterThan(22)
    expect(compactRows.memberScrollHeight).toBe(compactRows.memberClientHeight)
    expect(compactRows.memberBottom).toBeLessThanOrEqual(compactRows.diskTop)
    const popup = footer.locator('#status-details > div')
    await expect(popup).toBeVisible()
    const popupBox = await popup.boundingBox()
    if (!popupBox) throw new Error('compact status details did not render')
    expect(popupBox.x).toBeGreaterThanOrEqual(0)
    expect(popupBox.y).toBeGreaterThanOrEqual(0)
    expect(popupBox.x + popupBox.width).toBeLessThanOrEqual(size.width)
    expect(popupBox.y + popupBox.height).toBeLessThanOrEqual(size.height)
    await expect(popup).toHaveCSS('overflow-y', 'auto')

    // The readouts give way inside their own group rather than pushing it:
    // an overflowing left group is what took the controls off the edge.
    const overflow = await page.evaluate(() => {
      const left = document.querySelector('footer')?.firstElementChild
      return left ? left.scrollWidth - left.clientWidth : -1
    })
    expect(overflow).toBe(0)

    const verticalOverflow = await page.evaluate(() =>
      Math.max(
        document.documentElement.scrollHeight - document.documentElement.clientHeight,
        document.body.scrollHeight - document.body.clientHeight,
      ),
    )
    expect(verticalOverflow).toBe(0)
  }

  await page.setViewportSize(wideSize)
  await expect(
    footer.getByRole('button', { name: 'Show status details' }),
  ).toBeHidden()
  await expect(footer.getByRole('status', { name: unreachableNotice })).toBeVisible()
  for (const name of controls) {
    await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
  }
  const wideMember = footer.getByText(longName, { exact: true })
  const wideDisk = footer.locator('[aria-label="Disk usage"]')
  await expect(wideMember).toBeVisible()
  const wideMemberMetrics = await wideMember.evaluate((element) => {
    const memberElement = element as HTMLElement
    return {
      height: memberElement.clientHeight,
      clientWidth: memberElement.clientWidth,
      scrollWidth: memberElement.scrollWidth,
    }
  })
  expect(wideMemberMetrics.height).toBe(22)
  expect(wideMemberMetrics.scrollWidth).toBeGreaterThan(wideMemberMetrics.clientWidth)
  await expect(wideDisk).toHaveCSS('height', '22px')

  await page.setViewportSize(mobileSize)
  const mobileTrigger = footer.getByRole('button', { name: 'Show status details' })
  const mobileNotice = footer.getByRole('status', { name: unreachableNotice })
  await expect(mobileTrigger).toBeVisible()
  if ((await mobileTrigger.getAttribute('aria-expanded')) === 'true') {
    await mobileTrigger.focus()
    await mobileTrigger.press('Enter')
    await expect(mobileNotice).toBeHidden()
  }
  await mobileTrigger.focus()
  await mobileTrigger.press('Enter')
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

  const mobileVerticalOverflow = await page.evaluate(() =>
    Math.max(
      document.documentElement.scrollHeight - document.documentElement.clientHeight,
      document.body.scrollHeight - document.body.clientHeight,
    ),
  )
  expect(mobileVerticalOverflow).toBe(0)

  // Exercise the bottom-edge control with the same pointer sequence that can
  // lose the target when an active style grows the document.
  await page.setViewportSize({ width: 390, height: 844 })
  const theme = page.getByRole('button', { name: 'Theme: system' })
  await expect(theme).toBeVisible()
  const themeBox = await theme.boundingBox()
  if (!themeBox) throw new Error('theme toggle did not render')
  const pointer = {
    x: themeBox.x + themeBox.width / 2,
    y: themeBox.y + themeBox.height / 2,
  }
  await page.mouse.move(pointer.x, pointer.y)
  await page.mouse.down()
  await page.mouse.up()
  await expect(
    page.getByRole('button', { name: 'Theme: light' }),
  ).toBeVisible()
  const compactOverflow = await page.evaluate(() =>
    Math.max(
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
      document.body.scrollWidth - document.body.clientWidth,
    ),
  )
  expect(compactOverflow).toBe(0)
})
