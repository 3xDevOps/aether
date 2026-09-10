// Driving a select from a test, by the path this ticket is about. The mouse
// works too - `primitives.test.tsx` commits a choice that way - but every
// caller here is asserting what a keyboard reader gets.

import { fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/** Opens one select and answers with the list it dropped. */
export async function openSelect(trigger: HTMLElement): Promise<HTMLElement> {
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  return screen.findByRole('listbox')
}

/** Opens one select and picks the option with this name, from the keyboard.
 * Enter on the focused option, not a bare click: Radix reads the pointer type
 * it last saw, and a click with no pointer events before it takes the touch
 * path instead of either of the two this app is driven by. */
export async function pickOption(trigger: HTMLElement, option: string): Promise<void> {
  await openSelect(trigger)
  const item = await screen.findByRole('option', { name: option })
  item.focus()
  fireEvent.keyDown(item, { key: 'Enter' })
}
