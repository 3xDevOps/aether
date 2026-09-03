import { describe, expect, it } from 'vitest'
import { clampDockHeight } from '@/components/dock'

describe('clampDockHeight', () => {
  it('keeps dock heights between 120px and the viewport limit', () => {
    const originalHeight = window.innerHeight
    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      value: 900,
    })

    expect(clampDockHeight(0)).toBe(120)
    expect(clampDockHeight(500)).toBe(500)
    expect(clampDockHeight(900)).toBe(700)

    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      value: originalHeight,
    })
  })
})
