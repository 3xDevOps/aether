import { render, screen } from '@testing-library/react'
import { Button } from '@/components/ui/button'

test('draws the shared focus outline', () => {
  render(<Button>Save</Button>)

  expect(screen.getByRole('button').className).toContain('focus-visible:outline-2')
  expect(screen.getByRole('button').className).toContain('focus-visible:outline-ring')
})
