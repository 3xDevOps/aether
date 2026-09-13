/**
 * The colour a browser paints around the shell, per OS scheme. The layout's
 * viewport export hands both to the meta tag and the manifest takes the dark
 * one; a manifest carries a single `theme_color` where the meta tag carries
 * one per scheme.
 */
export const themeColor = {
  light: '#f8f8f8',
  dark: '#181818',
} as const

/**
 * The ground every Aether icon is drawn on, and so the manifest's
 * `background_color`: an installed app's splash centres one of those icons on
 * it, and any other value leaves the tile showing as a square. It is darker
 * than the dashboard's own dark `--background` (`#1f1f1f`) on purpose - a
 * launcher icon has to read as an object against a wallpaper, not blend into
 * a page - so the splash lightens slightly as the SPA paints over it.
 *
 * `scripts/make-icons.py` renders the tiles on this same hex, and the Electron
 * window uses it to avoid a white flash before first paint.
 */
export const iconBackground = '#0a0a0a'
