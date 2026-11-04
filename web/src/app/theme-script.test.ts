import { afterEach, describe, expect, it, vi } from 'vitest'
import { themeScript } from '@/app/theme-script'
import { createRootStore } from '@/store'

// The script runs before the store exists, so it re-implements the store's
// reading of `aether.ui`; every stored shape must resolve the same way in both.
describe('pre-paint theme script', () => {
  afterEach(() => {
    window.localStorage.removeItem('aether.ui')
    vi.unstubAllGlobals()
  })

  const cases: [string, unknown][] = [
    ['nothing stored', null],
    ['v11 system', { state: { theme: 'system' }, version: 11 }],
    ['v11 light', { state: { theme: 'light' }, version: 11 }],
    ['v12 system', { state: { theme: 'system' }, version: 12 }],
    ['v12 light', { state: { theme: 'light' }, version: 12 }],
    ['v12 dark', { state: { theme: 'dark' }, version: 12 }],
    ['no version, system', { state: { theme: 'system' } }],
    ['unknown theme', { state: { theme: 'blue' }, version: 12 }],
    ['corrupt JSON', '{'],
  ]

  for (const systemDark of [false, true]) {
    for (const [name, stored] of cases) {
      it(`agrees with the store for ${name} on a ${systemDark ? 'dark' : 'light'} system`, () => {
        vi.stubGlobal('matchMedia', (query: string) => ({ matches: systemDark && query.includes('dark'), addEventListener() {}, removeEventListener() {} }))
        if (stored !== null) window.localStorage.setItem('aether.ui', typeof stored === 'string' ? stored : JSON.stringify(stored))
        const theme = createRootStore().getState().theme
        if (stored !== null) window.localStorage.setItem('aether.ui', typeof stored === 'string' ? stored : JSON.stringify(stored))
        const root = document.documentElement
        root.className = ''
        new Function(themeScript)()
        expect(root.classList.contains('dark')).toBe(theme === 'dark' || (theme === 'system' && systemDark))
      })
    }
  }
})
