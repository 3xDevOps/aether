// The key bar's sticky modifiers. A soft keyboard sends characters rather
// than key codes, so Ctrl and Alt cannot be held: one tap applies a modifier
// to the next key, typed or tapped, and a second tap close behind the first
// locks it until it is tapped again.

export type ModifierState = 'off' | 'once' | 'locked'
export type ModifierKey = 'ctrl' | 'alt'
export type Modifiers = Record<ModifierKey, ModifierState>

export const noModifiers: Modifiers = { ctrl: 'off', alt: 'off' }

/** How close a second tap must follow the first to lock the modifier. */
export const lockWindowMs = 400

export function tapModifier(state: ModifierState, sinceLastTap: number): ModifierState {
  if (state === 'off') return 'once'
  if (state === 'once' && sinceLastTap <= lockWindowMs) return 'locked'
  return 'off'
}

/**
 * The control code one character carries under Ctrl, or null when it has
 * none. A terminal maps `@` through `_`, the ASCII letters, and space. Case
 * folding is done by hand rather than with `toUpperCase`, which is locale
 * text rather than ASCII: it turns the German sharp s into two letters and
 * the dotless i into an I, and either would send a control code nobody
 * pressed.
 */
function controlCode(data: string): string | null {
  const code = data.charCodeAt(0)
  if (code === 0x20) return '\x00'
  const upper = code >= 0x61 && code <= 0x7a ? code - 0x20 : code
  if (upper < 0x40 || upper > 0x5f) return null
  return String.fromCharCode(upper & 0x1f)
}

/**
 * What `data` sends under the active modifiers, or null when they do not
 * apply to it - a paste, or a key with no control code - so the caller can
 * keep them for the next key instead of spending them on this one. Cursor,
 * Home/End, paging and function keys take xterm's modifier parameter.
 */
export function applyModifiers(data: string, ctrl: boolean, alt: boolean): string | null {
  const param = 1 + (alt ? 2 : 0) + (ctrl ? 4 : 0)
  const cursor = /^\x1b[[O]([A-DHFPQRS])$/.exec(data)
  if (cursor) return `\x1b[1;${param}${cursor[1]}`
  const tilde = /^\x1b\[(\d+)~$/.exec(data)
  if (tilde) return `\x1b[${tilde[1]};${param}~`
  if (data.length !== 1) return null
  const base = ctrl ? controlCode(data) : data
  if (base === null) return null
  return alt ? `\x1b${base}` : base
}
