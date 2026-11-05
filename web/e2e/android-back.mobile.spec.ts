// What the Android app's back does on a page opened with nothing behind it,
// such as one a share opened: it runs routeHomeScript
// (android/.../Navigation.kt) in the page. On a phone that goes to Needs you
// without a reload, so whatever the page holds survives, and a second back
// finds home and leaves the app.

import { expect, test } from './mobile'

const routeHome =
  "history.replaceState(history.state, '', matchMedia('(max-width: 767px)').matches ? '/?page=needs-you' : '/'); dispatchEvent(new PopStateEvent('popstate'))"

test('the Android shell takes a page opened from outside home to Needs you without a reload', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  await page.goto(`${alice.url}&page=settings&id=notifications`)
  const tabs = page.getByRole('navigation', { name: 'Main navigation' })
  const needsYou = tabs.getByRole('button', { name: /^Needs you/ })
  await expect(page.getByLabel('Notify this device when a session needs me')).toBeVisible()
  await expect(needsYou).not.toHaveAttribute('aria-current', 'page')
  await page.evaluate(() => Object.assign(window, { notReloaded: true }))
  const entries = await page.evaluate(() => history.length)

  await page.evaluate(routeHome)
  await expect(needsYou).toHaveAttribute('aria-current', 'page')
  expect(new URL(page.url()).searchParams.get('page')).toBe('needs-you')
  expect(await page.evaluate(() => history.length)).toBe(entries)
  expect(await page.evaluate(() => (window as unknown as { notReloaded?: boolean }).notReloaded)).toBe(true)
})
