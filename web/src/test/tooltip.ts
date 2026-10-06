// A tooltip opens on focus only after a navigation key; jsdom cannot hover.

import { act, fireEvent, screen } from '@testing-library/react'
import { expect } from 'vitest'

export async function hintOn(control: HTMLElement): Promise<string> {
  fireEvent.keyDown(document.body, { key: 'Tab' })
  // Focusing is what opens it, so the state it sets belongs inside `act`.
  act(() => control.focus())

  const hint = await screen.findByRole('tooltip')
  // With the whole shell rendered, another control's tooltip may be the open one.
  expect(control.getAttribute('aria-describedby')).toBe(hint.id)
  return hint.textContent ?? ''
}
