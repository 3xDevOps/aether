// A real server shutdown exercises complete diagnostics in the bounded status
// popup at wide and cramped widths, without faking connection state.

import { expect, test } from './fixtures'
import { OnboardingWizard } from './pages/wizard'

/** Controls that must stay reachable even when the left status readouts wrap. */
const controls = ['Search runs and commands', 'Keyboard shortcuts', 'Show status details']

const wideSize = { width: 1280, height: 600 }
const sizes = [
  // The floor desktop/main.js enforces.
  { width: 960, height: 600 },
  // A browser tab, which has no floor at all.
  { width: 800, height: 480 },
  wideSize,
]
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
  const trigger = footer.getByRole('button', { name: 'Show status details' })
  await trigger.click()
  await expect(footer.locator('[aria-label="Disk usage"]')).toBeVisible()

  await aether.server.stop()

  for (const size of sizes) {
    await page.setViewportSize(size)
    for (const name of controls) {
      await expect(page.getByRole('button', { name })).toBeInViewport({ ratio: 1 })
    }
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
    const localStatus = footer.getByText('Not linked', { exact: true })
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
    const usage = footer.getByRole('button', { name: 'Usage', exact: true })
    await usage.scrollIntoViewIfNeeded()
    await expect(usage).toBeInViewport({ ratio: 1 })
  }

})
