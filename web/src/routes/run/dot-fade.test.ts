import { expect, it } from 'vitest'
import { dotFadeShape, dotFadeVars, fadesDots, FALLOFF, MIN_OPACITY, RADIUS_PX, RETURN_MS } from '@/routes/run/dot-fade'

it('centres the mask on the pointer, relative to the strip', () => {
  expect(dotFadeVars(250, 130, { left: 200, top: 100 })).toEqual({ '--dot-px': '50px', '--dot-py': '30px' })
  expect(dotFadeVars(190, 95, { left: 200, top: 100 })).toEqual({ '--dot-px': '-10px', '--dot-py': '-5px' })
})

it('shapes the mask from the tunables', () => {
  expect(dotFadeShape).toEqual({
    '--dot-radius': `${RADIUS_PX}px`,
    '--dot-inner': `${(1 - FALLOFF) * 100}%`,
    '--dot-min': String(MIN_OPACITY),
    '--dot-return': `${RETURN_MS}ms`,
  })
})

it('fades only for a fine pointer on a wide layout without reduced motion', () => {
  expect(fadesDots({ finePointer: true, phoneWidth: false, reducedMotion: false })).toBe(true)
  expect(fadesDots({ finePointer: false, phoneWidth: false, reducedMotion: false })).toBe(false)
  expect(fadesDots({ finePointer: true, phoneWidth: true, reducedMotion: false })).toBe(false)
  expect(fadesDots({ finePointer: true, phoneWidth: false, reducedMotion: true })).toBe(false)
})
