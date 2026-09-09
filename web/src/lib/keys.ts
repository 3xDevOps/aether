import type * as React from 'react'

/** How far one arrow key press moves a resize handle. */
const resizeStep = 16

/** Anything that owns the keys pressed inside it. */
const overlays =
  '.xterm, [role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"]'

/**
 * The subset that owns the keyboard outright while it is open. A terminal does
 * not: it is on the page throughout a run and only takes what is typed into
 * it. A dialog on its way out does not either - Radix keeps it mounted for the
 * length of its exit animation. An open select list is one of these and has
 * to be: it is portalled out of whatever hosts it, so there is no dialog above
 * it to stand a chord down, and it does not close with one either.
 */
const open = ':not([data-state="closed"])'
const modals = [
  ...['dialog', 'alertdialog', 'menu'].map((role) => `[role="${role}"]${open}`),
  // A select list says when it is open; cmdk's list carries no state at all,
  // and the palette's own rows must not stand down the chord that closes it.
  '[role="listbox"][data-state="open"]',
].join(', ')

/** True inside anything that takes typing, a select's typeahead included. A
 * select is a `combobox` rather than a tag of its own: it is a button, and it
 * answers a bare letter by jumping to the option that starts with it. */
function inField(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  if (target.isContentEditable) return true
  if (target.getAttribute('role') === 'combobox') return true
  return ['INPUT', 'TEXTAREA'].includes(target.tagName)
}

/**
 * True when the key landed inside a terminal, a menu or a dialog. Read from
 * the event target, never from what is open; see docs/dashboard-frontend.md.
 * The exception is a key that landed on `body`, where focus falls when the
 * element holding it is removed: there is no target left to ask, so the
 * document is asked instead.
 */
export function inOverlay(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  if (target.closest(overlays)) return true
  return target === document.body && document.querySelector(modals) !== null
}

/**
 * True when the key landed inside something that takes the keyboard outright.
 * Narrower than `inOverlay` by exactly the terminal, which owns what is typed
 * into it but has no claim on a modified shortcut.
 */
export function inModal(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  if (target.closest(modals)) return true
  return target === document.body && document.querySelector(modals) !== null
}

/** True while the keyboard belongs to something other than the shell. */
export function keyboardBusy(event: KeyboardEvent): boolean {
  return inField(event.target) || inOverlay(event.target)
}

/** Which tab an Arrow/Home/End press moves to, or null when the strip does
 * not own the key. Horizontal strips wrap. */
function tabListTarget(key: string, count: number, index: number): number | null {
  if (count === 0) return null
  switch (key) {
    case 'ArrowRight':
      return (index + 1) % count
    case 'ArrowLeft':
      return (index - 1 + count) % count
    case 'Home':
      return 0
    case 'End':
      return count - 1
    default:
      return null
  }
}

/** Moves focus along a tab list, and nothing else: opening one of these tabs
 * costs a mount, so an arrow key must not do it. */
export function onTabListKeyDown(
  event: React.KeyboardEvent<HTMLElement>,
  count: number,
  index: number,
  onFocus: (next: number) => void,
): void {
  if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
  const next = tabListTarget(event.key, count, index)
  if (next === null) return
  event.preventDefault()
  onFocus(next)
  const tabs = event.currentTarget
    .closest('[role="tablist"]')
    ?.querySelectorAll<HTMLElement>('[role="tab"]')
  tabs?.[next]?.focus()
}

/** One resize handle's bounds and the arrow key that grows the pane it sizes. */
interface Splitter {
  value: number
  min: number
  max: number
  grow: string
  shrink: string
}

/** Where an Arrow/Home/End press moves a resize handle, or null when the
 * handle does not own the key. Home and End are the pane's own bounds. */
export function splitterTarget(key: string, splitter: Splitter): number | null {
  switch (key) {
    case splitter.grow:
      return splitter.value + resizeStep
    case splitter.shrink:
      return splitter.value - resizeStep
    case 'Home':
      return splitter.min
    case 'End':
      return splitter.max
    default:
      return null
  }
}
