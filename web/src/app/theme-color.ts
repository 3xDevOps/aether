/** Browser chrome colour per resolved theme; the page starts on the dark default, and a manifest carries one `theme_color`, the dark one. */
export const themeColor = {
  light: '#f7f7f7',
  dark: '#1b1c1d',
} as const

/**
 * The ground the icons are drawn on, so the splash tile does not show as a
 * square. Darker than `--canvas` on purpose; `scripts/make-icons.py` uses the same hex.
 */
export const iconBackground = '#0a0a0a'
