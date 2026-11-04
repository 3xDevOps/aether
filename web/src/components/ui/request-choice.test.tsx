import { render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'
import { DecisionButtons } from '@/components/decision-buttons'
import { RequestAction, RequestActions } from '@/components/ui/request-choice'
import { useStore } from '@/store'

afterEach(() => useStore.setState({ staleSince: null }))

it('disables an action on a saved snapshot and says why', () => {
  useStore.setState({ staleSince: Date.now() })
  render(<RequestAction label="Approve" />)

  const button = screen.getByRole('button', { name: 'Approve' })
  expect((button as HTMLButtonElement).disabled).toBe(true)
  expect(button.getAttribute('title')).toBe('Reconnect to act')
})

it('writes the reason once beside a stale row of actions', () => {
  useStore.setState({ staleSince: Date.now() })
  render(
    <RequestActions>
      <DecisionButtons decide={async () => {}} />
      <RequestAction label="Reply" />
    </RequestActions>,
  )

  expect(screen.getAllByText('Reconnect to act')).toHaveLength(1)
})

it('leaves an action live once the state is live', () => {
  render(<RequestAction label="Approve" />)

  expect((screen.getByRole('button', { name: 'Approve' }) as HTMLButtonElement).disabled).toBe(false)
})
