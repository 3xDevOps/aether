// jsdom has no layout, so a virtualized list sees a zero-height viewport,
// mounts no rows and keeps unmeasured rows hidden. This reports every
// observed list item at `row` pixels and anything else at the viewport size,
// which is what virtua reads to decide how many rows fill the viewport.

import { afterEach } from 'vitest'

const original = globalThis.ResizeObserver
const offsetParent = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')

export function sizedViewport(height = 600, row = 32): void {
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', {
    configurable: true,
    get(this: HTMLElement) {
      return this.parentElement
    },
  })
  globalThis.ResizeObserver = class {
    constructor(private readonly callback: ResizeObserverCallback) {}
    observe(target: Element) {
      const size = target.tagName === 'LI' ? row : height
      queueMicrotask(() =>
        this.callback(
          [{ target, contentRect: { width: 800, height: size } } as unknown as ResizeObserverEntry],
          this as unknown as ResizeObserver,
        ),
      )
    }
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

afterEach(() => {
  globalThis.ResizeObserver = original
  if (offsetParent) Object.defineProperty(HTMLElement.prototype, 'offsetParent', offsetParent)
})
