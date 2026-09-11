import { atViewport } from '@/test/viewport'

test('answers width and pointer queries for the screen it was given', () => {
  atViewport(390, 'coarse')

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
  const seen: boolean[] = []
  window
    .matchMedia('(max-width: 1000px)')
    .addEventListener('change', (event) => seen.push(event.matches))

  resize(1200)
  resize(1400)
  resize(800)

  expect(seen).toEqual([false, true])
})
