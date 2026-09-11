import { atViewport } from '@/test/viewport'

test('answers width and pointer queries for the screen it was given', () => {
  atViewport(390, { height: 844, pointer: 'coarse' })

  expect(window.innerWidth).toBe(390)
  expect(window.innerHeight).toBe(844)

  expect(window.matchMedia('(max-width: 640px)').matches).toBe(true)
  expect(window.matchMedia('(min-width: 768px)').matches).toBe(false)
  expect(window.matchMedia('(pointer: coarse)').matches).toBe(true)
  expect(window.matchMedia('(pointer: fine)').matches).toBe(false)
  expect(
    window.matchMedia('(min-width: 320px) and (pointer: coarse)').matches,
  ).toBe(true)
  // A feature this does not model keeps the always-false answer from
  // setup.ts rather than guessing at it.
  expect(window.matchMedia('(prefers-color-scheme: dark)').matches).toBe(false)
})

test('tells a listener when a resize changes its answer, and only then', () => {
  const resize = atViewport(1000)
  const narrow = window.matchMedia('(max-width: 1000px)')
  const seen: boolean[] = []
  narrow.addEventListener('change', (event) => seen.push(event.matches))
  const resized: number[] = []
  const onResize = () => resized.push(window.innerWidth)
  window.addEventListener('resize', onResize)
  onTestFinished(() => window.removeEventListener('resize', onResize))

  resize(1200)
  resize(1400)
  resize(800)

  expect(seen).toEqual([false, true])
  expect(resized).toEqual([1200, 1400, 800])
  // A list read after the resize answers for the width it was read at, not
  // the one it was created at.
  expect(narrow.matches).toBe(true)
})
