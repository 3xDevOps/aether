/** True on macOS, where the modifier prints as the command glyph. */
function isMac(): boolean {
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } }
  return /mac/i.test(nav.userAgentData?.platform ?? nav.platform)
}

/** A modifier shortcut as this platform writes it. */
export function shortcutLabel(key: string): string {
  return isMac() ? `⌘${key}` : `Ctrl+${key}`
}
