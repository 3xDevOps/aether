import type { MetadataRoute } from 'next'

import { iconBackground, themeColor } from './theme-color'

// Next builds a metadata file into a route handler, and `output: 'export'`
// refuses to collect one that has not said it is static.
export const dynamic = 'force-static'

/**
 * What both mobile browsers read to keep the dashboard on a home screen. Why
 * these fields and why no service worker ships is in
 * `docs/dashboard-frontend.md`; the constraint here is that the colours come
 * from `theme-color.ts` rather than being written twice, because the layout's
 * viewport export and the icon script hold the other copies.
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
