// Saved launch choices live on the server: Bob saves an account in the
// launch dialog on a desktop browser, and a phone-sized browser of his opens
// on it in Settings and the launch dialog. Alice, the other member, keeps her
// own. Once Alice stops sharing, Bob's saved account shows as unusable with
// the server's reason on both screens.
//
// Nothing here launches a run, so the spec needs no Docker daemon.

import type { Browser, Locator, Page } from '@playwright/test'

import { expect, test } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

const shots = process.env.AETHER_E2E_SHOTS
const desktop = { viewport: { width: 1440, height: 900 } }
const phone = { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true }

async function openLaunch(page: Page, onPhone: boolean): Promise<Locator> {
  const button = onPhone
    ? page.getByRole('banner').getByRole('button', { name: 'New session' })
    : page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'New session' })
  await button.click()
  const dialog = page.getByRole('dialog', { name: 'New session' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('radio', { name: /^custom/ })).toBeVisible()
  return dialog
}

async function contextPage(browser: Browser, options: typeof desktop | typeof phone, url: string): Promise<Page> {
  const page = await (await browser.newContext(options)).newPage()
  await page.goto(url)
  return page
}

test('saved launch choices follow a member to every device, and each member keeps their own', async ({ page, browser, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('shop')
  await seedWorkspace(alice, aether.server.addr, repo)
  const invite = await aether.invite(alice)
  const bob = await aether.member('bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite, name: 'bob' })
  const aliceID = await memberID(alice)
  const bobID = await memberID(bob)
  aether.installAgent(aliceID, 'claude')
  aether.installAgent(bobID, 'claude')
  const aliceName = (await alice.api.rpc<{ member: { display_name: string } }>('server.info')).member.display_name
  await alice.api.rpc('account.share', { member_id: bobID })

  const bobDesktop = await contextPage(browser, desktop, bob.url)
  let dialog = await openLaunch(bobDesktop, false)
  await dialog.getByRole('button', { name: /^Options/ }).click()
  await dialog.getByRole('combobox', { name: 'Account', exact: true }).click()
  await bobDesktop.getByRole('option', { name: `${aliceName} (shared)` }).click()
  await dialog.getByRole('checkbox', { name: 'Fall back to another account' }).click()
  await dialog.getByRole('button', { name: 'Save as my default' }).click()
  await expect(dialog.getByText('Saved for every device.')).toBeVisible()
  if (shots) await bobDesktop.screenshot({ path: `${shots}/dialog-saved-desktop.png` })
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  const bobPhone = await contextPage(browser, phone, `${bob.url}&page=settings&id=saved-launch-defaults`)
  const accountSelect = bobPhone.getByRole('combobox', { name: 'Account' })
  await expect(accountSelect).toHaveText(aliceName)
  await expect(bobPhone.getByRole('combobox', { name: 'Fallback' })).toHaveText('Off')
  if (shots) await bobPhone.screenshot({ path: `${shots}/settings-phone.png`, fullPage: true })
  await bobPhone.goto(bob.url)
  dialog = await openLaunch(bobPhone, true)
  await expect(dialog.getByRole('button', { name: /^Options/ })).toContainText(`Account: ${aliceName} (shared) · Fallback off`)
  await dialog.getByRole('button', { name: /^Options/ }).scrollIntoViewIfNeeded()
  if (shots) await bobPhone.screenshot({ path: `${shots}/dialog-phone.png` })

  await page.goto(`${alice.url}&page=settings&id=saved-launch-defaults`)
  await expect(page.getByText(/Nothing saved yet/)).toBeVisible()
  await expect(page.getByRole('combobox', { name: 'Account' })).toHaveText('Your own account')

  await alice.api.rpc('account.revoke', { member_id: bobID })
  const reason = `has not shared their account with you`
  await bobPhone.goto(`${bob.url}&page=settings&id=saved-launch-defaults`)
  await expect(bobPhone.getByText('Not usable now')).toBeVisible()
  await expect(bobPhone.getByText(new RegExp(reason))).toBeVisible()
  if (shots) await bobPhone.screenshot({ path: `${shots}/settings-revoked-phone.png`, fullPage: true })

  await bobDesktop.reload()
  dialog = await openLaunch(bobDesktop, false)
  await expect(dialog.getByText('Saved defaults not used')).toBeVisible()
  await expect(dialog.getByText(new RegExp(reason))).toBeVisible()
  await dialog.getByText('Saved defaults not used').scrollIntoViewIfNeeded()
  if (shots) await bobDesktop.screenshot({ path: `${shots}/dialog-revoked-desktop.png` })
  await bobDesktop.goto(`${bob.url}&page=settings&id=saved-launch-defaults`)
  await expect(bobDesktop.getByText('Not usable now')).toBeVisible()
  if (shots) await bobDesktop.screenshot({ path: `${shots}/settings-revoked-desktop.png`, fullPage: true })
})
