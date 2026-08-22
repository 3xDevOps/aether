// Terminal font handling shared by the run terminal and the workspace shell.
//
// The terminals render agent TUIs, which draw Nerd Font symbols (powerline
// separators, devicons, codicons) from the Unicode private use area. No
// system font covers those, so the SPA ships the symbols-only Nerd Font
// (declared in index.css) and slots it between the text stack and the
// generic fallback: text glyphs keep coming from the platform monospace and
// only the symbols fall through to the shipped font.
export const terminalFontFamily =
  'ui-monospace, SFMono-Regular, Menlo, Consolas, "Symbols Nerd Font Mono", monospace'

/**
 * A glyph inside the shipped font's unicode-range (powerline right arrow).
 * FontFaceSet.load/check sample a plain space by default, which the range
 * does not cover - probing with this glyph is what actually starts the load.
 */
const sampleGlyph = '\ue0b0'

/** How long a cold font load may hold a terminal shut. */
const loadTimeoutMs = 2000

/**
 * Runs `open` once the shipped symbols font is usable, synchronously when it
 * already is or the browser has no font API (jsdom). xterm measures and
 * caches glyph metrics synchronously at `Terminal.open`, so opening before
 * the font arrives would bake fallback metrics in for the terminal's whole
 * life. A hung load opens anyway after a bounded wait: a terminal with
 * placeholder symbols beats no terminal. Returns a cancel function; a
 * cancelled wait never opens.
 */
export function whenTerminalFontReady(open: () => void): () => void {
  const fonts = document.fonts
  if (!fonts?.load || fonts.check(`12px "Symbols Nerd Font Mono"`, sampleGlyph)) {
    open()
    return () => {}
  }
  let cancelled = false
  const { promise: expired, resolve: expire } = Promise.withResolvers<void>()
  const timer = setTimeout(expire, loadTimeoutMs)
  const loading = fonts.load(`12px "Symbols Nerd Font Mono"`, sampleGlyph).catch(() => {
    // A failed fetch falls back to placeholder glyphs, not a dead pane.
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
