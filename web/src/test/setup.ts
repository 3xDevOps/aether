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

// jsdom drops a stylesheet from `document.styleSheets` only when the <style>
// element itself is removed while still connected. xterm nests its three
// style elements inside the terminal it disposes of wholesale, so every
// terminal a test mounts orphans three sheets - about 800 rules - in that
// list for the rest of the file, and `getComputedStyle`, which
// testing-library runs on every visibility query, re-cascades the whole pile.
// The sheets cannot be unregistered (`ownerNode` hands back jsdom's internal
// node, which has no parent to remove it from), so empty them instead.
afterEach(() => {
  for (const sheet of document.styleSheets) {
    if (sheet.ownerNode?.isConnected !== false) continue
    while (sheet.cssRules.length > 0) sheet.deleteRule(0)
  }
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
