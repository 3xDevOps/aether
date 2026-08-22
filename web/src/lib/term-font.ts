// Terminal font handling shared by the run terminal and the workspace shell.
//
// The terminals render agent TUIs, which draw Nerd Font symbols (powerline
// separators, devicons, codicons) from the Unicode private use area. No
// system font covers those, so the SPA ships JetBrainsMono Nerd Font Mono
// (declared in index.css) as the terminal's primary font.
//
// It must be the primary font, not a symbols-only overlay behind the platform
// monospace: xterm's DOM renderer squeezes every glyph into the text font's
// cell with negative letter-spacing, so symbols from an overlay font with a
// wider advance (the symbols-only Nerd Font is full-em, text cells are
// ~0.6em) get their right edge sliced off. In a patched Mono font, text and
// symbols share one advance and every glyph fits the cell exactly.
export const terminalFontFamily = '"JetBrainsMono NFM", monospace'

/**
 * A glyph the shipped font must supply (powerline right arrow).
 * FontFaceSet.load/check sample a plain space by default; probing with a PUA
 * glyph makes the check honest and is what actually starts the load.
 */
const sampleGlyph = '\ue0b0'

/** How long a cold font load may hold a terminal shut. */
const loadTimeoutMs = 2000

/** Both shipped faces; bold must be ready too or xterm synthesizes it. */
const faces = [`12px "JetBrainsMono NFM"`, `bold 12px "JetBrainsMono NFM"`]

/**
 * Runs `open` once the shipped terminal font is usable, synchronously when it
 * already is or the browser has no font API (jsdom). xterm measures and
 * caches glyph metrics synchronously at `Terminal.open`, so opening before
 * the font arrives would bake fallback metrics in for the terminal's whole
 * life. A hung load opens anyway after a bounded wait: a terminal with
 * fallback glyphs beats no terminal. Returns a cancel function; a cancelled
 * wait never opens.
 */
export function whenTerminalFontReady(open: () => void): () => void {
  const fonts = document.fonts
  if (!fonts?.load || faces.every((face) => fonts.check(face, sampleGlyph))) {
    open()
    return () => {}
  }
  let cancelled = false
  const { promise: expired, resolve: expire } = Promise.withResolvers<void>()
  const timer = setTimeout(expire, loadTimeoutMs)
  const loading = Promise.all(faces.map((face) => fonts.load(face, sampleGlyph))).catch(() => {
    // A failed fetch falls back to platform glyphs, not a dead pane.
  })
  void Promise.race([loading, expired]).then(() => {
    clearTimeout(timer)
    if (!cancelled) open()
  })
  return () => {
    cancelled = true
    clearTimeout(timer)
  }
}
