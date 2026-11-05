// What the Android app's back does on a page opened with nothing behind it,
// such as one a share opened: it runs routeHomeScript
// (android/.../MainActivity.kt) in the page. The dashboard must route home
// on it without a reload, so whatever the page holds survives.

import { expect, test } from './mobile'

const routeHome = "history.replaceState(history.state, '', '/'); dispatchEvent(new PopStateEvent('popstate'))"

test('the Android shell takes a page opened from outside home to the Board without a reload', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  await page.goto(`${alice.url}&page=settings&id=notifications`)
  const board = page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Board', exact: true })
  await expect(page.getByLabel('Notify this device when a session needs me')).toBeVisible()
  await expect(board).not.toHaveAttribute('aria-current', 'page')
  await page.evaluate(() => Object.assign(window, { notReloaded: true }))

  await page.evaluate(routeHome)
  await expect(board).toHaveAttribute('aria-current', 'page')
  expect(new URL(page.url()).search).toBe('')
  expect(await page.evaluate(() => (window as unknown as { notReloaded?: boolean }).notReloaded)).toBe(true)
})
