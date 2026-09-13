import type { Metadata, Viewport } from 'next'
import type { ReactNode } from 'react'
import '../index.css'
import { themeColor } from './theme-color'

/**
 * `apple-mobile-web-app-capable` is spelled out by hand, beside the
 * `appleWebApp.capable` that looks like the same thing: Next 16 emits only
 * the unprefixed `mobile-web-app-capable` for that field, and Apple's status
 * bar tag has no effect without the prefixed one.
 */
export const metadata: Metadata = {
  title: 'Aether',
  description: 'Aether developer workbench',
  applicationName: 'Aether',
  icons: { apple: '/icons/apple-touch-icon.png' },
  appleWebApp: { capable: true, title: 'Aether', statusBarStyle: 'black-translucent' },
  other: { 'apple-mobile-web-app-capable': 'yes' },
}

/**
 * `viewportFit: 'cover'` and the translucent status bar above hand the shell
 * the screen edges the system used to keep back, and nothing here pads them:
 * every surface that reaches an edge insets itself, the top one through
 * `--safe-top` in `src/index.css`. See `docs/dashboard-frontend.md` for what
 * each field buys.
 */
export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
  viewportFit: 'cover',
  interactiveWidget: 'resizes-content',
  themeColor: [
    { media: '(prefers-color-scheme: light)', color: themeColor.light },
    { media: '(prefers-color-scheme: dark)', color: themeColor.dark },
  ],
}

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="en">
      <body className="h-full">
        <div id="root" className="h-full">
          {children}
        </div>
      </body>
    </html>
  )
}
