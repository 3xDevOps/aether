import { render, screen } from '@testing-library/react'
import { Button } from '@/components/ui/button'

test('uses a compact but visible focus ring', () => {
  render(<Button>Save</Button>)

  expect(screen.getByRole('button').className).toContain('focus-visible:ring-[2px]')
})
