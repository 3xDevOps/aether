"use client"

import { useEffect } from 'react'
import { useStore } from '@/store'

const darkQuery = '(prefers-color-scheme: dark)'


export function ThemeEffect() {
  const theme = useStore((s) => s.theme)
  const textSize = useStore((s) => s.textSize)

  useEffect(() => {
    document.documentElement.dataset.textSize = textSize
  }, [textSize])

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

