import { render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'
import { RequestAction } from '@/components/ui/request-choice'
import { useStore } from '@/store'

afterEach(() => useStore.setState({ staleSince: null }))

it('disables an action on a saved snapshot and says why', () => {
  useStore.setState({ staleSince: Date.now() })
  render(<RequestAction label="Approve" />)

  const button = screen.getByRole('button', { name: 'Approve' })
  expect((button as HTMLButtonElement).disabled).toBe(true)
  expect(button.getAttribute('title')).toBe('Reconnect to act')
})

it('leaves an action live once the state is live', () => {
  render(<RequestAction label="Approve" />)

  expect((screen.getByRole('button', { name: 'Approve' }) as HTMLButtonElement).disabled).toBe(false)
})
