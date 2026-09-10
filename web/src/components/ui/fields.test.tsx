import { render, screen } from '@testing-library/react'
import { createRef } from 'react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

test('Label names the control it wraps, with no id on either', () => {
  render(
    <Label>
      Task
      <Input />
    </Label>,
  )

  expect(screen.getByLabelText('Task').tagName).toBe('INPUT')
})

// Four call sites focus or select a field through a ref. Were forwarding to
// break, every one of them would become a silent no-op.
test('a ref reaches the field itself', () => {
  const ref = createRef<HTMLInputElement>()

  render(<Input ref={ref} />)

  expect(ref.current?.tagName).toBe('INPUT')
})

