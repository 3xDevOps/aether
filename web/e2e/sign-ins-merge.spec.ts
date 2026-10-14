// Profile > Sign-ins > Merge across two real members: one gets a merge code,
// the other enters it and previews. Every member this harness creates joins
// with its own SSH key, and a member has at most one, so the server refuses
// the preview; the spec checks that refusal reaches the dashboard verbatim.
// Choosing settings, proposing and accepting are covered by
// src/routes/members/sign-ins.test.tsx.

import type { Page } from '@playwright/test'

import { expect, test } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

async function openMerge(page: Page, phone = false) {
  if (phone) await page.getByRole('button', { name: /^Open sidebar/ }).click()
  await page.getByRole(phone ? 'dialog' : 'navigation', { name: 'Aether' })
    .getByRole('button', { name: /, (Live|Reconnecting|Offline)$/ }).click()
  await page.getByRole('menuitem', { name: 'Profile' }).click()
  const signIns = page.getByRole('dialog', { name: 'Profile' }).getByRole('region', { name: 'Sign-ins' })
  await expect(signIns.getByRole('list', { name: 'Your sign-ins' })).toContainText('SSH key')
  await signIns.getByRole('button', { name: 'Merge with another member' }).click()
  return page.getByRole('dialog', { name: 'Merge with another member' })
}

test('a merge code from one member is previewed by another, and the refusal is shown verbatim', async ({ page, browser, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  const invite = await aether.invite(alice)
  const bob = await aether.member('bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite, name: 'bob' })
  const [aliceID, bobID] = await Promise.all([memberID(alice), memberID(bob)])

  await page.goto(alice.url)
  const minting = await openMerge(page)
  await minting.getByRole('button', { name: 'Get a merge code' }).click()
  const code = (await minting.getByRole('group', { name: 'Merge code' }).locator('code').textContent())?.trim() ?? ''
  expect(code).toMatch(/^[A-Z0-9]{4}(-[A-Z0-9]{4}){3}$/)
  await expect(minting.getByRole('timer')).toContainText(/This code expires in (10:00|9:\d\d)/)
  await expect(minting).toContainText(`aether member merge ${code}`)

  const bobContext = await browser.newContext({ viewport: { width: 390, height: 844 } })
  const bobPage = await bobContext.newPage()
  try {
    await bobPage.goto(bob.url)
    const entering = await openMerge(bobPage, true)
    await entering.getByRole('tab', { name: 'I have a merge code' }).click()
    await entering.getByRole('textbox', { name: 'Merge code' }).fill(code)
    await entering.getByRole('button', { name: 'Preview' }).click()
    await expect(entering.getByRole('alert')).toContainText(
      `both members have an SSH public key (${aliceID} and ${bobID}) and a member has at most one; reach the server with one key and merge the other sign-in`,
    )
    await expect(entering.getByRole('button', { name: 'Propose merge' })).toHaveCount(0)
    await bobPage.screenshot({ path: test.info().outputPath('merge-preview-refused-phone.png'), fullPage: true })
  } finally {
    await bobContext.close()
  }
})
