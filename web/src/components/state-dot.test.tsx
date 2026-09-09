import { render, screen } from '@testing-library/react'
import { StateIndicator } from '@/components/state-dot'
import { stateLabel, type PresentationState } from '@/lib/status'

const still = (Object.keys(stateLabel) as PresentationState[]).filter(
  (state) => state !== 'working',
)

describe('state indicator', () => {
  it('bounces three dots while a run is working', () => {
    const { container } = render(<StateIndicator state="working" />)

    expect(container.querySelectorAll('.working-dots span')).toHaveLength(3)
    expect(screen.getByLabelText('Working')).toBeDefined()
  })

  it.each(still)('keeps %s on the static dot', (state) => {
    const { container } = render(<StateIndicator state={state} />)

    expect(screen.getByRole('img', { name: stateLabel[state] })).toBeDefined()
    expect(container.querySelector('.working-dots')).toBeNull()
  })

  // Board rows and cards carry no state in words, so the mark is what a
  // screen reader has; the run header prints the label itself.
  it('says nothing of its own where the state is already in words', () => {
    const { container } = render(<StateIndicator state="working" decorative />)

    expect(screen.queryByLabelText('Working')).toBeNull()
    expect(container.querySelector('[aria-hidden="true"]')).not.toBeNull()
  })
})
