import type { Metadata, Viewport } from 'next'
import type { ReactNode } from 'react'
import '../index.css'
import { themeColor } from './theme-color'

/**
 * `appleWebApp` is what an iPhone reads when someone picks Share > Add to
 * Home Screen; iOS has no manifest support, so the name, the standalone
 * window and the icon all have to be said again in meta tags.
 * `black-translucent` puts the shell under the status bar, which is the half
 * of `viewport-fit=cover` iOS needs, and the safe-area padding already in the
 * title bar pads it back out.
 */
export const metadata: Metadata = {
  title: 'Aether',
  description: 'Aether developer workbench',
  applicationName: 'Aether',
  icons: { apple: '/icons/apple-touch-icon.png' },
  appleWebApp: { capable: true, title: 'Aether', statusBarStyle: 'black-translucent' },
}

/**
 * The shell is a fixed column that never scrolls the document, so a phone has
 * to be told three things Next's default meta does not say.
 *
 * `viewport-fit=cover` lets the shell paint under the notch and the home
 * indicator; the title bar and status bar pad themselves back out with
 * `env(safe-area-inset-*)`. `interactive-widget=resizes-content` makes the
 * soft keyboard shrink the layout viewport, which is what every `dvh` in the
 * app - dialogs, the palette, selects - is already sized against. Without it
 * the keyboard covers a dialog footer instead of shortening the dialog.
 *
 * `themeColor` follows the OS scheme rather than the app's own theme setting:
 * the browser reads it before the SPA has applied a stored preference.
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
