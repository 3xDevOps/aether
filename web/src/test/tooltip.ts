// Reading a control's tooltip in a test. HeroUI's tooltip is React Aria's, and
// it shows one only for a reader it believes is on the keyboard - which it
// decides from the last key it saw. So the key comes first, then the focus.
// Hover is not driveable here: jsdom produces no pointer modality, so the
// pointer path belongs to the browser suite.

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
