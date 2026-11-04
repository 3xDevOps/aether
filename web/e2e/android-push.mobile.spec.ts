// The push switch inside the Android app, whose WebView has no Web Push: the
// page asks the app through window.AetherAndroid instead. The app's side is
// a stand-in answering as android/.../PushBridge.kt does; the dashboard,
// gateway and server are real. This is the Edge gateway's shape too, where a
// browser gets no switch at all.

import { expect, test } from './mobile'

test('the Android app names a missing distributor, then turns notifications on', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  await page.addInitScript(() => {
    let installed = false
    const bridge = Object.assign(new EventTarget(), {
      postMessage(message: string) {
        const { op } = JSON.parse(message) as { op: string }
        const send = (state: object) =>
          setTimeout(() => bridge.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(state) })))
        if (!installed) {
          return send(op === 'enable' ? { kind: 'no-distributor', error: 'No UnifiedPush distributor is installed on this phone.' } : { kind: 'no-distributor' })
        }
        if (op === 'enable') send({ kind: 'pending' })
        send(op === 'enable' ? { kind: 'on', endpoint: 'https://ntfy.sh/upAetherE2E?up=1' } : { kind: 'off' })
      },
    })
    Object.assign(window, { AetherAndroid: bridge, installDistributor: () => (installed = true) })
  })

  await page.goto(`${alice.url}&page=settings&id=notifications`)
  await expect(page.getByText('The app gets notifications through a UnifiedPush distributor, and this phone has none.')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Android notifications guide' })).toHaveAttribute('href', /networking\.md#in-the-android-app$/)
  const toggle = page.getByLabel('Notify this device when a session needs me')
  await expect(toggle).toBeEnabled()
  // Edge's gateway: no installed Chrome dashboard to point to.
  await expect(page.getByText('Or open this dashboard in Chrome')).toHaveCount(0)
  await toggle.click()
  await expect(page.getByRole('region', { name: 'Push notifications' }).getByRole('alert')).toHaveText('No UnifiedPush distributor is installed on this phone.')
  await expect(toggle).not.toBeChecked()

  await page.evaluate(() => (window as unknown as { installDistributor: () => void }).installDistributor())
  await toggle.click()
  await expect(toggle).toBeChecked()
  await expect(page.getByRole('button', { name: 'Send test notification' })).toBeVisible()
})
