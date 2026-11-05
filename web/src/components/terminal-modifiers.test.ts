import { applyModifiers, lockWindowMs, tapModifier } from '@/components/terminal-modifiers'

describe('sticky modifiers', () => {
  it('arm once on a tap, lock on a quick second tap, and release on the next', () => {
    expect(tapModifier('off', Infinity)).toBe('once')
    expect(tapModifier('once', lockWindowMs)).toBe('locked')
    expect(tapModifier('once', lockWindowMs + 1)).toBe('off')
    expect(tapModifier('locked', 10)).toBe('off')
  })

  it('send what a keyboard sends for the key under them', () => {
    expect(applyModifiers('c', true, false)).toBe('\x03')
    expect(applyModifiers(' ', true, false)).toBe('\x00')
    expect(applyModifiers('`', true, false)).toBe('\x00')
    expect(applyModifiers('?', true, false)).toBe('\x7f')
    expect(applyModifiers('\x7f', true, false)).toBe('\x08')
    expect(applyModifiers('b', false, true)).toBe('\x1bb')
    expect(applyModifiers('x', true, true)).toBe('\x1b\x18')
    expect(applyModifiers('\x1b[D', true, false)).toBe('\x1b[1;5D')
    expect(applyModifiers('\x1bOA', false, true)).toBe('\x1b[1;3A')
    expect(applyModifiers('\x1b[5~', true, true)).toBe('\x1b[5;7~')
    // Nothing a terminal would send: the caller keeps the modifier.
    expect(applyModifiers('1', true, false)).toBeNull()
    expect(applyModifiers('paste', true, false)).toBeNull()
  })
})
