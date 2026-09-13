import type { MetadataRoute } from 'next'

import { iconBackground, themeColor } from './theme-color'

// Next builds a metadata file into a route handler, and `output: 'export'`
// refuses to collect one that has not said it is static.
export const dynamic = 'force-static'

/**
 * What a phone needs to keep the dashboard on its home screen. Next emits
 * this as `/manifest.webmanifest` in the static export and links it from the
 * page, so the server binary serves it out of the embedded bundle like every
 * other asset. Both mobile browsers read it: Safari has since iOS 11.3, so
 * `display`, `start_url` and these icons are what give an iPhone its
 * standalone window too, not the Apple meta tags in the layout.
 *
 * No service worker ships with it. Chromium's installability check no longer
 * looks for one - the blog post that announced the removal scopes it to
 * installing "from the menu, since version 108 on mobile and 112 on Desktop"
 * (https://developer.chrome.com/blog/update-install-criteria), and today's
 * check has no reference to service workers at all. The dashboard could not
 * cache anyway: the SPA lives inside the server binary and has to change the
 * moment the binary does.
 *
 * 192px and 512px are what Chrome and MDN document; the bar the code actually
 * enforces is lower, one `any` icon of at least 144px. Shipping both
 * documented sizes costs nothing and is what every other consumer expects.
 *
 * `theme_color` is the dark value because a manifest holds one where the
 * layout's viewport export holds one per scheme; once the page has loaded,
 * Chrome follows the per-scheme meta tag instead.
 */
export default function manifest(): MetadataRoute.Manifest {
  return {
    name: 'Aether',
    short_name: 'Aether',
    description: 'Aether developer workbench',
    start_url: '/',
    scope: '/',
    display: 'standalone',
    background_color: iconBackground,
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
