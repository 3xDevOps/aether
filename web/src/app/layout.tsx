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
  themeColor: themeColor.dark,
}

/**
 * Applies the stored theme before the first paint, so a light device does not
 * flash light before React mounts `ThemeEffect`. It mirrors the persisted
 * `aether.ui` state and its version 12 migration in `src/store/index.ts`.
 */
const themeScript = `(() => {
  let theme = 'dark'
  try {
    const stored = JSON.parse(localStorage.getItem('aether.ui') ?? 'null')
    const saved = stored?.state?.theme
    if (saved === 'light' || (saved === 'system' && stored.version >= 12)) theme = saved
  } catch {}
  const dark = theme === 'dark' || (theme === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
  const root = document.documentElement
  root.classList.toggle('dark', dark)
  root.dataset.theme = dark ? 'dark' : 'light'
  root.style.colorScheme = dark ? 'dark' : 'light'
})()`

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="en" className="dark" data-theme="dark" style={{ colorScheme: 'dark' }} suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeScript }} />
      </head>
      <body className="h-full">
        <div id="root" className="h-full">
          {children}
        </div>
      </body>
    </html>
  )
}
