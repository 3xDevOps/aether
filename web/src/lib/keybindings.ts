// Every dashboard shortcut, as data. The table drives behaviour, tooltips and
// the shortcuts dialog; see "Keyboard and focus" in docs/dashboard-frontend.md.

import { useEffect, useLayoutEffect, useRef } from 'react'
import { matchKeybindingPress, parseKeybinding } from 'tinykeys'
import { activeScopes, pushScope, type KeyScope } from '@/lib/key-scope'
import { inModal, inOverlay, keyboardBusy } from '@/lib/keys'
import { useStore } from '@/store'

export interface Keybinding {
  id: string
  /** tinykeys syntax: `$mod` is Cmd on Apple platforms and Ctrl elsewhere,
   * and a space separates the two presses of a sequence. */
  keys: string
  scope: KeyScope
  label: string
  /** A modified binding's own gate. Unmodified bindings always stand down
   * while a field, terminal or overlay has the keyboard. */
  when?: (event: KeyboardEvent) => boolean
}

const mac = parseKeybinding('$mod+a')[0]![0].includes('Meta')

function inBrowser(event: KeyboardEvent): boolean {
  return event.target instanceof HTMLElement && event.target.closest('[data-browser]') !== null
}

export const keybindings = [
  { id: 'palette', keys: '$mod+K', scope: 'global', label: 'Open the command palette' },
  { id: 'palette-alt', keys: '$mod+Shift+P', scope: 'global', label: 'Open the command palette' },
  {
    id: 'sidebar',
    keys: '$mod+B',
    scope: 'global',
    label: 'Show or hide the sidebar',
    // A terminal keeps it: Ctrl+B is a common tmux prefix.
    when: (event) => !keyboardBusy(event) && !inModal(event.target),
  },
  { id: 'shortcuts', keys: '[Shift]+?', scope: 'global', label: 'Open this reference' },
  { id: 'launch', keys: 'n', scope: 'global', label: 'New run' },
  { id: 'next-needs-you', keys: 'u', scope: 'global', label: 'Open the next run that needs you' },
  { id: 'row-next', keys: 'j', scope: 'global', label: 'Focus the next run in the sidebar' },
  { id: 'row-previous', keys: 'k', scope: 'global', label: 'Focus the previous run in the sidebar' },
  { id: 'go-board', keys: 'g b', scope: 'global', label: 'Go to the board' },
  { id: 'go-overview', keys: 'g l', scope: 'global', label: 'Go to all workspaces' },
  { id: 'go-missions', keys: 'g s', scope: 'global', label: 'Go to swarms' },
  { id: 'go-activity', keys: 'g a', scope: 'global', label: 'Go to activity' },
  { id: 'go-files', keys: 'g f', scope: 'global', label: 'Go to files' },
  { id: 'go-agents', keys: 'g g', scope: 'global', label: 'Go to agents' },
  { id: 'go-environment', keys: 'g e', scope: 'global', label: 'Go to your environment' },
  { id: 'go-settings', keys: 'g ,', scope: 'global', label: 'Go to settings' },
  { id: 'run-view-next', keys: ']', scope: 'run', label: 'Show the next run view' },
  { id: 'run-view-previous', keys: '[', scope: 'run', label: 'Show the previous run view' },
  {
    id: 'run-details',
    keys: '$mod+.',
    scope: 'run',
    label: 'Show or hide run details',
    when: (event) => !inModal(event.target),
  },
  { id: 'focus-composer', keys: 'c', scope: 'run', label: 'Message the agent' },
  { id: 'leave-run', keys: 'Escape', scope: 'run', label: 'Leave a run for the board' },
  {
    id: 'browser-address',
    keys: '$mod+L',
    scope: 'browser',
    label: 'Focus the address bar, from anywhere in the run',
    // A terminal keeps it: Ctrl+L clears the screen.
    when: (event) => !inOverlay(event.target),
  },
  { id: 'browser-reload', keys: '$mod+R', scope: 'browser', label: 'Reload the page', when: inBrowser },
  // The platform's own history keys: on macOS Alt and an arrow moves a caret by a word.
  { id: 'browser-back', keys: mac ? '$mod+[' : 'Alt+ArrowLeft', scope: 'browser', label: 'Go back', when: inBrowser },
  { id: 'browser-forward', keys: mac ? '$mod+]' : 'Alt+ArrowRight', scope: 'browser', label: 'Go forward', when: inBrowser },
  { id: 'composer-send', keys: '$mod+Enter', scope: 'composer', label: 'Send the message, or steer the running turn' },
  { id: 'composer-queue', keys: '$mod+Shift+Enter', scope: 'composer', label: 'Queue the message for after this turn' },
  { id: 'request-option-1', keys: '1', scope: 'request', label: 'Pick option 1 of the focused request' },
  { id: 'request-option-2', keys: '2', scope: 'request', label: 'Pick option 2 of the focused request' },
  { id: 'request-option-3', keys: '3', scope: 'request', label: 'Pick option 3 of the focused request' },
  { id: 'request-option-4', keys: '4', scope: 'request', label: 'Pick option 4 of the focused request' },
  { id: 'card-approve', keys: 'a', scope: 'card', label: "Approve the focused card's request" },
  { id: 'card-reply', keys: 'r', scope: 'card', label: "Reply to the focused card's agent" },
  { id: 'card-open', keys: 'o', scope: 'card', label: 'Open the focused card' },
] as const satisfies readonly Keybinding[]

export type KeybindingID = (typeof keybindings)[number]['id']

type Handler = (event: KeyboardEvent) => void
type ScopeHandlers<S extends KeyScope> = Partial<
  Record<Extract<(typeof keybindings)[number], { scope: S }>['id'], Handler>
>

/** How long the first press of a sequence waits for the second, in ms. */
const sequenceTimeout = 1500

const presses = new Map(keybindings.map((binding) => [binding.id, parseKeybinding(binding.keys)]))

function pressesOf(binding: Keybinding) {
  return presses.get(binding.id as KeybindingID)!
}

/** A chord needs a modifier beyond Shift. Chords are matched while the event
 * is still capturing, before a terminal's own handler turns them into input. */
export function isChord(binding: Keybinding): boolean {
  return pressesOf(binding)[0]![0].some((modifier) => modifier !== 'Shift')
}

/** A character key with no chord: what Settings > Single-key shortcuts
 * turns off. Escape is not a character and stays live. */
export function isSingleKey(binding: Keybinding): boolean {
  return !isChord(binding) && pressesOf(binding).every(([, , key]) => String(key).length === 1)
}

const modifierLabels: Record<string, string> = { Control: 'Ctrl', Meta: '⌘', Alt: 'Alt', Shift: 'Shift' }
const keyLabels: Record<string, string> = { Escape: 'Esc', Enter: 'Enter', ArrowLeft: '←', ArrowRight: '→' }

/** A tinykeys string as this platform writes it: `⌘K` on macOS, `Ctrl+K`
 * elsewhere, `g then b` for a sequence. Optional modifiers are left out. */
export function formatKeys(keys: string): string {
  return parseKeybinding(keys)
    .map(([required, , key]) => {
      const name = keyLabels[String(key)] ?? (required.length > 0 ? String(key).toUpperCase() : String(key))
      const parts = [...required.map((modifier) => modifierLabels[modifier] ?? modifier), name]
      return mac && parts[0] === '⌘' ? `⌘${parts.slice(1).join('+')}` : parts.join('+')
    })
    .join(' then ')
}

export function shortcutLabel(id: KeybindingID): string {
  return formatKeys(keybindings.find((binding) => binding.id === id)!.keys)
}

/** Whether `event` is the single press of binding `id`, for a layer that
 * stands the table down and answers one of its keys itself. */
export function isPress(event: KeyboardEvent, id: KeybindingID): boolean {
  const parsed = presses.get(id)!
  return parsed.length === 1 && matchKeybindingPress(event, parsed[0]!)
}

/** The live handlers for chords or for single keys, innermost scope first. */
function candidates(chord: boolean): [Keybinding, Handler][] {
  const out: [Keybinding, Handler][] = []
  for (const entry of activeScopes()) {
    for (const binding of keybindings) {
      if (binding.scope !== entry.scope || isChord(binding) !== chord) continue
      const handler = entry.handlers.current[binding.id]
      if (handler) out.push([binding, handler])
    }
  }
  return out
}

function onChord(event: KeyboardEvent) {
  if (event.isComposing) return
  for (const [binding, handler] of candidates(true)) {
    if (!matchKeybindingPress(event, pressesOf(binding)[0]!)) continue
    if (!binding.when || binding.when(event)) handler(event)
    return
  }
}

const modifierKeys = ['Shift', 'Control', 'Alt', 'Meta']
let pending: { ids: string[]; until: number } | null = null

function onKey(event: KeyboardEvent) {
  // Reaching for Shift is its own keydown and must not end a sequence.
  if (event.isComposing || modifierKeys.includes(event.key)) return
  // Read and clear first: a `g` abandoned in a terminal or under a dialog
  // must not complete on the next key the shell does see.
  const prefix = pending && Date.now() < pending.until ? pending.ids : null
  pending = null
  if (event.defaultPrevented || keyboardBusy(event) || inModal(event.target)) return
  const singleKeys = useStore.getState().singleKeyShortcuts
  const live = candidates(false).filter(([binding]) => singleKeys || !isSingleKey(binding))
  if (prefix) {
    // A pending first press takes the next key whatever it is: Escape there
    // cancels the sequence rather than leaving the run.
    const hit = live.find(([binding]) =>
      prefix.includes(binding.id) && matchKeybindingPress(event, pressesOf(binding)[1]!))
    if (!hit) return
    event.preventDefault()
    hit[1](event)
    return
  }
  const starts = live.filter(([binding]) => matchKeybindingPress(event, pressesOf(binding)[0]!))
  if (starts.length === 0) return
  const [first, handler] = starts[0]!
  if (pressesOf(first).length === 1) {
    event.preventDefault()
    handler(event)
    return
  }
  pending = { ids: starts.map(([binding]) => binding.id), until: Date.now() + sequenceTimeout }
}

let mounted = 0

/** Chords listen on the capturing pass, so a terminal never sees them; single
 * keys on the bubbling pass, so a component that handles the key first can
 * mark it handled. */
export function useKeybindings<S extends KeyScope>(scope: S, handlers: ScopeHandlers<S>): void {
  const latest = useRef<ScopeHandlers<S>>(handlers)
  useLayoutEffect(() => {
    latest.current = handlers
  })
  useEffect(() => {
    const pop = pushScope({ scope, handlers: latest })
    if (mounted++ === 0) {
      window.addEventListener('keydown', onChord, true)
      window.addEventListener('keydown', onKey)
    }
    return () => {
      pop()
      if (--mounted === 0) {
        window.removeEventListener('keydown', onChord, true)
        window.removeEventListener('keydown', onKey)
        pending = null
      }
    }
  }, [scope])
}

/** Whether the shortcuts dialog lists a binding: it answers right now, or
 * its scope is not on screen to ask (a run's keys, read from the board). */
export function listed(binding: Keybinding): boolean {
  const entries = activeScopes().filter((entry) => entry.scope === binding.scope)
  return entries.length === 0 || entries.some((entry) => entry.handlers.current[binding.id])
}
