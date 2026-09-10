// jsdom implements no media queries; components that ask for the colour
// scheme (theme toggle, sonner) need one to render at all.
if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}

// The store persists view preferences under one localStorage key, and
// `activeWorkspace` is now one of them: without this, the workspace a test
// hydrated into would still be the scope of the next test's fresh store.
beforeEach(() => {
  window.localStorage.clear()
})

// Radix measures, scrolls and captures the pointer over whatever it pops out -
// an open select, a dialog, a menu - and xterm's fit addon measures its host.
// jsdom implements none of these, and a component that reaches for one throws
// before it renders.
// Assigned rather than stubbed, so a file calling `vi.unstubAllGlobals()`
// restores this rather than taking it away.
Element.prototype.scrollIntoView = vi.fn()
Element.prototype.hasPointerCapture = () => false
Element.prototype.releasePointerCapture = () => {}
globalThis.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver
