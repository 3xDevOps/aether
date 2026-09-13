// The dashboard as an app on a phone's home screen. What Chrome needs before
// it offers to install one is the manifest and its icons, fetched with no
// credential of any kind, so the gateway has to serve all of them to an
// anonymous request with the right content types.
//
// The standalone window itself is not emulable here. Chromium exposes no
// `display-mode` override - not through Playwright's `emulateMedia`, not
// through CDP's `Emulation.setEmulatedMedia` features, and not through the
// `PWA` domain, which headless does not carry - so the closest this suite
// gets is what the mobile project already is: a phone-sized viewport with no
// browser chrome around it. The shell is asserted whole in that viewport
// below; a real installed window stays a manual check on a phone.

import { seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

interface ManifestIcon {
  src: string
  sizes: string
  purpose: string
}

test('a phone is served everything it needs to install the dashboard', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)

  await page.goto(alice.url)
  await expect(page.locator('link[rel="manifest"]')).toHaveAttribute(
    'href',
    '/manifest.webmanifest',
  )

  // `page.request` carries none of the page's context, which is the point:
  // the browser fetches these before anyone is signed in, and the installed
  // app starts at `/` with no token in the URL.
  const origin = new URL(alice.url).origin
  const response = await page.request.get(origin + '/manifest.webmanifest')
  expect(response.status()).toBe(200)
  expect(response.headers()['content-type']).toBe('application/manifest+json')

  const manifest = JSON.parse(await response.text())
  expect(manifest).toMatchObject({
    name: 'Aether',
    start_url: '/',
    display: 'standalone',
  })

  // Chrome will not offer to install an app without a 192px and a 512px
  // icon, and every icon named has to actually be there.
  const icons: ManifestIcon[] = manifest.icons
  expect(icons.map((icon) => icon.sizes)).toEqual(
    expect.arrayContaining(['192x192', '512x512']),
  )
  expect(icons.map((icon) => icon.purpose)).toContain('maskable')
  for (const icon of icons) {
    const image = await page.request.get(origin + icon.src)
    expect(image.status(), icon.src).toBe(200)
    expect(image.headers()['content-type'], icon.src).toBe('image/png')
  }

  // iOS reads no manifest; its home-screen icon is the link tag alone.
  const apple = '/icons/apple-touch-icon.png'
  await expect(page.locator('link[rel="apple-touch-icon"]')).toHaveAttribute('href', apple)
  expect((await page.request.get(origin + apple)).status()).toBe(200)

  // The shell draws its own title bar and status bar, so an installed window
  // loses nothing when the browser's chrome goes: on a phone-sized viewport
  // with none of it, everything is still reachable and nothing overflows.
  await expect(page.getByRole('contentinfo')).toBeInViewport({ ratio: 1 })
  await expect(page.getByRole('button', { name: 'Expand sidebar' })).toBeVisible()
  const overflow = await page.evaluate(() =>
    Math.max(
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
      document.documentElement.scrollHeight - document.documentElement.clientHeight,
    ),
  )
  expect(overflow).toBe(0)
})
