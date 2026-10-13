import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/** Opens a model picker and chooses the item with this accessible name. */
export async function pickModel(trigger: HTMLElement, option: string | RegExp): Promise<void> {
  await userEvent.click(trigger)
  await userEvent.click(await screen.findByRole('option', { name: option }))
}

/** Opens a model picker and enters a model id that no list offers. */
export async function chooseModel(trigger: HTMLElement, text: string): Promise<void> {
  await userEvent.click(trigger)
  await userEvent.type(await screen.findByLabelText('Search models'), text)
  await userEvent.click(await screen.findByRole('option', { name: `Use “${text}”` }))
}
