import type { MetadataRoute } from 'next'

import { themeColor } from './theme-color'

/**
 * What a phone needs to keep the dashboard on its home screen. Next emits
 * this as `/manifest.webmanifest` in the static export and links it from the
 * page, so the server binary serves it out of the embedded bundle like every
 * other asset.
 *
 * Chrome's install criteria are the manifest alone - a name, a `start_url`, a
 * standalone display and a 192px and a 512px icon. A service worker stopped
 * being one of them in Chrome 108 on Android and 112 on desktop
 * (https://developer.chrome.com/blog/update-install-criteria), so the
 * dashboard ships none. It could not cache anyway: the SPA lives inside the
 * server binary and has to change the moment the binary does.
 *
 * `theme_color` is the dark value because a manifest holds one and the
 * layout's viewport export holds one per scheme. This is what the OS paints
 * around the splash before the page loads; once it has, Chrome follows the
 * per-scheme meta tag instead.
 */
// Next builds a metadata file into a route handler, and `output: 'export'`
// refuses to collect one that has not said it is static.
export const dynamic = 'force-static'

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: 'Aether',
    short_name: 'Aether',
    description: 'Aether developer workbench',
    start_url: '/',
    scope: '/',
    display: 'standalone',
    background_color: themeColor.dark,
    theme_color: themeColor.dark,
    icons: [
      { src: '/icons/icon-192.png', sizes: '192x192', type: 'image/png', purpose: 'any' },
      { src: '/icons/icon-512.png', sizes: '512x512', type: 'image/png', purpose: 'any' },
      {
        src: '/icons/icon-maskable-192.png',
        sizes: '192x192',
        type: 'image/png',
        purpose: 'maskable',
      },
      {
        src: '/icons/icon-maskable-512.png',
        sizes: '512x512',
        type: 'image/png',
        purpose: 'maskable',
      },
    ],
  }
}
