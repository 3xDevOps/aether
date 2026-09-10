"use client"

import { Monitor, Moon, Sun } from 'lucide-react'
import { useEffect } from 'react'
import { Button } from '@/components/ui/button'
import { Tooltip } from '@/components/ui/heroui'
import { useStore } from '@/store'
import type { Theme } from '@/store/ui'

const darkQuery = '(prefers-color-scheme: dark)'


/** Keeps the document's dark class in sync with the theme preference. */
export function ThemeEffect() {
  const theme = useStore((s) => s.theme)

  useEffect(() => {
    const apply = () => {
      const systemDark =
        typeof window !== 'undefined' &&
        (window.matchMedia?.(darkQuery).matches ?? false)
      const dark = theme === 'dark' || (theme === 'system' && systemDark)
      const root = document.documentElement
      root.classList.toggle('dark', dark)
      root.dataset.theme = dark ? 'dark' : 'light'
      root.style.colorScheme = dark ? 'dark' : 'light'
    }
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
    <Tooltip>
      <Tooltip.Trigger<'button'>
        render={(triggerProps) => (
          <Button
            {...triggerProps}
            type="button"
            variant="ghost"
            size="icon"
            aria-label={`Theme: ${theme}`}
            onClick={(event) => {
              triggerProps.onClick?.(event)
              setTheme(order[(order.indexOf(theme) + 1) % order.length])
            }}
            className="size-[22px] min-h-[22px] min-w-[22px] rounded-sm"
          >
            <Icon className="size-3.5" />
          </Button>
        )}
      />
      <Tooltip.Content>Theme: {theme}</Tooltip.Content>
    </Tooltip>
  )
}
