import { parseKeybinding } from 'tinykeys'
import type { KeyScope } from '@/lib/key-scope'
import { formatKeys, isSingleKey, keybindings, type Keybinding } from '@/lib/keybindings'

// Global keys are live everywhere; a request card and the composer sit
// inside a run, never inside each other.
function overlap(a: KeyScope, b: KeyScope): boolean {
  if (a === b || a === 'global' || b === 'global') return true
  return a === 'run' || b === 'run'
}

function presses(binding: Keybinding): string[] {
  return parseKeybinding(binding.keys).map(([required, , key]) =>
    [...[...required].sort(), String(key).toLowerCase()].join('+'))
}

/** True when one sequence is the other or begins it: either way one of the
 * two can never fire. */
function collide(a: string[], b: string[]): boolean {
  const n = Math.min(a.length, b.length)
  return a.slice(0, n).join(' ') === b.slice(0, n).join(' ')
}

describe('keybinding table', () => {
  it('gives every binding in overlapping scopes its own keys', () => {
    const clashes: string[] = []
    keybindings.forEach((a, i) => {
      keybindings.slice(i + 1).forEach((b) => {
        if (overlap(a.scope, b.scope) && collide(presses(a), presses(b))) {
          clashes.push(`${a.id} (${a.keys}) and ${b.id} (${b.keys})`)
        }
      })
    })
    expect(clashes).toEqual([])
  })

  it('keeps ids unique and sequences to two presses', () => {
    expect(new Set(keybindings.map((b) => b.id)).size).toBe(keybindings.length)
    for (const binding of keybindings) expect(presses(binding).length).toBeLessThanOrEqual(2)
  })

  it('writes keys the way this platform reads them', () => {
    expect(formatKeys('$mod+K')).toBe('Ctrl+K')
    expect(formatKeys('$mod+Shift+M')).toBe('Ctrl+Shift+M')
    expect(formatKeys('g b')).toBe('g then b')
    expect(formatKeys('[Shift]+?')).toBe('?')
    expect(formatKeys('Escape')).toBe('Esc')
  })

  it('counts character keys, not Escape or chords, as single-key shortcuts', () => {
    const single = keybindings.filter(isSingleKey).map((b) => b.id)
    expect(single).toContain('launch')
    expect(single).toContain('go-board')
    expect(single).toContain('shortcuts')
    expect(single).not.toContain('leave-run')
    expect(single).not.toContain('palette')
  })
})
