// One viewport for a component test. jsdom has no layout, so `setup.ts`
// answers every media query with `false` and components render their widest
// layout. `atViewport` answers the width and pointer queries for a screen of
// a given size instead, which is what a narrow or touch layout needs.
//
// The browser suite (`web/e2e/`) stays the layer that proves real layout:
// this decides which branch a component renders, not how it looks.

import { act } from '@testing-library/react'

/** What a screen's input is: a finger is coarse, a mouse is fine. */
export type Pointer = 'coarse' | 'fine'

type Listener = (event: MediaQueryListEvent) => void

const widthFeature = /^\((min|max)-width:\s*(\d+)px\)$/
const pointerFeature = /^\((?:any-)?pointer:\s*(coarse|fine)\)$/

function evaluate(query: string, width: number, pointer: Pointer): boolean {
  return query.split(' and ').every((term) => {
    const bound = widthFeature.exec(term.trim())
    if (bound) {
      const px = Number(bound[2])
      return bound[1] === 'min' ? width >= px : width <= px
    }
    const input = pointerFeature.exec(term.trim())
    if (input) return input[1] === pointer
    // Anything else keeps the always-false answer from `setup.ts`: the
    // colour scheme and reduced motion are not what a viewport decides.
    return false
  })
}

/**
 * Evaluates width and pointer media queries against one screen for the rest
 * of the test. Returns the resize a component listens for: it re-evaluates
 * every query the component asked for and fires `change` on those whose
 * answer moved.
 */
export function atViewport(
  width: number,
  pointer: Pointer = 'fine',
): (width: number) => void {
  const listeners = new Map<string, Set<Listener>>()
  const asked = (query: string) => {
    const existing = listeners.get(query)
    if (existing) return existing
    const fresh = new Set<Listener>()
    listeners.set(query, fresh)
    return fresh
  }
  let current = width

  const previous = window.matchMedia
  onTestFinished(() => {
    window.matchMedia = previous
  })
  window.matchMedia = (query: string) =>
    ({
      matches: evaluate(query, current, pointer),
      media: query,
      onchange: null,
      addEventListener: (_: string, fn: Listener) => asked(query).add(fn),
      removeEventListener: (_: string, fn: Listener) => asked(query).delete(fn),
      addListener: (fn: Listener) => asked(query).add(fn),
      removeListener: (fn: Listener) => asked(query).delete(fn),
      dispatchEvent: () => false,
    }) as MediaQueryList

  return (next: number) => {
    const before = new Map(
      [...listeners.keys()].map((query) => [
        query,
        evaluate(query, current, pointer),
      ]),
    )
    current = next
    act(() => {
      for (const [query, fns] of listeners) {
        const matches = evaluate(query, current, pointer)
        if (matches === before.get(query)) continue
        for (const fn of fns) fn({ matches, media: query } as MediaQueryListEvent)
      }
    })
  }
}
