// jsdom has no layout, so a virtualized list sees a zero-height viewport,
// mounts no rows and keeps unmeasured rows hidden. This reports every
// observed list item at `row` pixels and anything else at the viewport size,
// which is what virtua reads to decide how many rows fill the viewport.

import { afterEach } from 'vitest'

const original = globalThis.ResizeObserver
const offsetParent = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')

/** Returns a resize: every observed element reports its size again. */
export function sizedViewport(height = 600, row = 32): () => void {
  Object.defineProperty(HTMLElement.prototype, 'offsetParent', {
    configurable: true,
    get(this: HTMLElement) {
      return this.parentElement
    },
  })
  const reports: (() => void)[] = []
  globalThis.ResizeObserver = class {
    constructor(private readonly callback: ResizeObserverCallback) {}
    observe(target: Element) {
      const size = target.tagName === 'LI' ? row : height
      const report = () =>
        this.callback(
          [{ target, contentRect: { width: 800, height: size } } as unknown as ResizeObserverEntry],
          this as unknown as ResizeObserver,
        )
      reports.push(report)
      queueMicrotask(report)
    }
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
  return () => reports.forEach((report) => report())
}

afterEach(() => {
  globalThis.ResizeObserver = original
  if (offsetParent) Object.defineProperty(HTMLElement.prototype, 'offsetParent', offsetParent)
})
