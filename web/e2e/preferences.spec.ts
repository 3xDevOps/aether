// Private assistant preferences through Settings → Memory against a real
// server: a member adds, edits and forgets one, and a second member never
// sees it. Data is synthetic.

import { expect, test } from './fixtures'
import { seedWorkspace } from './harness/setup'

test('a member adds, edits and forgets a preference that a teammate never sees', async ({ page, browser, aether }, testInfo) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  const invite = await aether.invite(alice)
  const bob = await aether.member('bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite, name: 'bob' })
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${alice.url}&page=settings&id=memory`)
  await expect(page.getByText(/Only you can see these\./)).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Nothing remembered yet' })).toBeVisible()

  await page.getByRole('textbox', { name: 'Add a preference' }).fill('Prefer table output')
  await page.getByRole('button', { name: 'Add', exact: true }).click()
  const list = page.getByRole('list', { name: 'Preferences' })
  const row = list.getByRole('listitem').first()
  await expect(row).toContainText('Added in Settings')
  await expect(row).toContainText('All my workspaces')
  await page.screenshot({ path: testInfo.outputPath('memory-desktop.png') })
  await testInfo.attach('memory desktop', { body: await page.screenshot(), contentType: 'image/png' })

  await row.getByRole('button', { name: 'Edit' }).click()
  const box = row.getByRole('textbox', { name: 'Preference text' })
  await box.fill('Prefer JSON output please')
  await row.getByRole('button', { name: 'Save' }).click()
  await expect(list.getByText('Prefer JSON output please')).toBeVisible()

  const { preferences } = await alice.api.rpc<{ preferences: { id: string; text: string }[] }>('preference.list', {})
  expect(preferences.map((p) => p.text)).toEqual(['Prefer JSON output please'])
  const id = preferences[0].id

  const other = await (await browser.newContext()).newPage()
  await other.goto(`${bob.url}&page=settings&id=memory`)
  await expect(other.getByRole('heading', { name: 'Nothing remembered yet' })).toBeVisible()
  await expect(other.getByText('Prefer JSON output please')).toHaveCount(0)
  const seen = await bob.api.rpc<{ preferences: unknown[] }>('preference.list', { include_disabled: true })
  expect(seen.preferences).toEqual([])
  await other.close()

  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`${alice.url}&page=settings&id=memory&entry=${id}`)
  await expect(page.locator(`#preference-${id}`)).toBeVisible()
  await testInfo.attach('memory mobile', { body: await page.screenshot(), contentType: 'image/png' })
  await page.locator(`#preference-${id}`).getByRole('button', { name: 'Actions' }).click()
  await page.getByRole('menuitem', { name: 'Forget' }).click()
  await expect(page.getByText(/Forget this preference\?/)).toBeVisible()
  await page.getByRole('button', { name: 'Forget', exact: true }).click()
  await expect(page.getByText('Forgotten: Prefer JSON output please')).toBeVisible()
  await expect(page.getByText(/Server backups and update snapshots taken earlier may still contain it/)).toBeVisible()
  const after = await alice.api.rpc<{ preferences: unknown[] }>('preference.list', { include_disabled: true })
  expect(after.preferences).toEqual([])
  expect(errors).toEqual([])
})
