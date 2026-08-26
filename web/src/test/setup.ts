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
