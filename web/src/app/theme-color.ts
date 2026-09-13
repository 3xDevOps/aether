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
