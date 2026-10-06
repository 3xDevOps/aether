// A tooltip opens on focus only after a navigation key; jsdom cannot hover.

import { act, fireEvent, screen } from '@testing-library/react'
import { expect } from 'vitest'

export async function hintOn(control: HTMLElement): Promise<string> {
  fireEvent.keyDown(document.body, { key: 'Tab' })
  // Focusing is what opens it, so the state it sets belongs inside `act`.
  act(() => control.focus())

  const hint = await screen.findByRole('tooltip')
  // This control's hint, not whichever one happens to be open: in a test that
  // renders the whole shell those are not the same question.
  expect(control.getAttribute('aria-describedby')).toBe(hint.id)
  return hint.textContent ?? ''
}
