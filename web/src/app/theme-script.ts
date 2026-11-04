import { themeColor } from './theme-color'

/**
 * Applies the stored theme before the first paint, so a light device does not
 * flash light before React mounts `ThemeEffect`. It mirrors the persisted
 * `aether.ui` state, its version 12 migration and its theme validation in
 * `src/store/index.ts`; `theme-script.test.ts` holds the two together.
 */
export const themeScript = `(() => {
  let theme = 'dark'
  try {
    const stored = JSON.parse(localStorage.getItem('aether.ui') ?? 'null')
    const saved = stored?.state?.theme
    if (saved === 'light' || (saved === 'system' && !(stored.version < 12))) theme = saved
  } catch {}
  const dark = theme === 'dark' || (theme === 'system' && window.matchMedia?.('(prefers-color-scheme: dark)').matches === true)
  const root = document.documentElement
  root.classList.toggle('dark', dark)
  root.dataset.theme = dark ? 'dark' : 'light'
  root.style.colorScheme = dark ? 'dark' : 'light'
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', dark ? '${themeColor.dark}' : '${themeColor.light}')
})()`
