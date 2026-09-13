// The dashboard as an app on a phone's home screen: what a browser fetches
// before it will offer to install one, and what the shell does with the
// screen edges an installed window hands it.
//
// The standalone window itself is not emulable here. Chromium exposes no
// `display-mode` override - not through Playwright's `emulateMedia`, not
// through CDP's `Emulation.setEmulatedMedia` features, and not through the
// `PWA` domain, which headless does not carry - so the closest this suite
// gets is what the mobile project already is: a phone-sized viewport with no
// browser chrome around it. A real installed window stays a manual check.

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

  // These requests carry neither of the gateway's credentials, a bearer
  // header or a `?token=` query, because that is the shape that matters: a
  // browser fetches the manifest and the icons before anyone is signed in.
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

  // Every icon the manifest names has to actually be served.
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

  // Safari prefers an `apple-touch-icon` over the manifest's icons.
  const apple = '/icons/apple-touch-icon.png'
  await expect(page.locator('link[rel="apple-touch-icon"]')).toHaveAttribute('href', apple)
  expect((await page.request.get(origin + apple)).status()).toBe(200)

  // The shell draws its own title bar and status bar, so nothing is lost
  // when the browser's chrome goes: everything stays reachable and nothing
  // overflows.
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
