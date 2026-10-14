import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

export async function pickModel(trigger: HTMLElement, option: string | RegExp): Promise<void> {
  await userEvent.click(trigger)
  await userEvent.click(await screen.findByRole('option', { name: option }))
}

export async function chooseModel(trigger: HTMLElement, text: string): Promise<void> {
  await userEvent.click(trigger)
  await userEvent.type(await screen.findByLabelText('Search models'), text)
  await userEvent.click(await screen.findByRole('option', { name: `Use “${text}”` }))
}
