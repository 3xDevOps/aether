import { Monitor, Moon, Sun } from 'lucide-react'
import { useEffect } from 'react'
import { Button } from '@/components/ui/button'
import { useStore } from '@/store'
import type { Theme } from '@/store/ui'

const darkQuery = '(prefers-color-scheme: dark)'

function prefersDark(): boolean {
  return window.matchMedia?.(darkQuery).matches ?? false
}

/** Keeps the document's dark class in sync with the theme preference. */
export function ThemeEffect() {
  const theme = useStore((s) => s.theme)

  useEffect(() => {
    const apply = () =>
      document.documentElement.classList.toggle(
        'dark',
        theme === 'dark' || (theme === 'system' && prefersDark()),
      )
    apply()
    if (theme !== 'system') return
    const media = window.matchMedia?.(darkQuery)
    media?.addEventListener('change', apply)
    return () => media?.removeEventListener('change', apply)
  }, [theme])

  return null
}

const order: Theme[] = ['system', 'light', 'dark']
const icons = { system: Monitor, light: Sun, dark: Moon }

export function ThemeToggle() {
  const theme = useStore((s) => s.theme)
  const setTheme = useStore((s) => s.setTheme)
  const Icon = icons[theme]
  return (
    <Button
      variant="ghost"
      size="icon"
      title={`Theme: ${theme}`}
      aria-label={`Theme: ${theme}`}
      onClick={() => setTheme(order[(order.indexOf(theme) + 1) % order.length])}
    >
      <Icon />
    </Button>
  )
}
