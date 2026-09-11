// The two browser events `onWake` listens for, as a browser delivers them.

/** A foreground return, or the network coming back. */
export function fire(event: 'visibilitychange' | 'online'): void {
  const target = event === 'online' ? window : document
  target.dispatchEvent(new Event(event))
}

/**
 * The other half of `visibilitychange`: the tab being backgrounded, which
 * fires the same event with the state reversed. jsdom reports a document as
 * visible and has no way to change it, so the getter is replaced for the one
 * dispatch and put back.
 */
export function fireHidden(): void {
  const own = Object.getOwnPropertyDescriptor(document, 'visibilityState')
  Object.defineProperty(document, 'visibilityState', {
    configurable: true,
    get: () => 'hidden',
  })
  try {
    document.dispatchEvent(new Event('visibilitychange'))
  } finally {
    if (own) Object.defineProperty(document, 'visibilityState', own)
    else Reflect.deleteProperty(document, 'visibilityState')
  }
}
