import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import { TakeoverDialog } from '@/routes/terminal/takeover-dialog'

function InterruptedController() {
  const [open, setOpen] = useState(false)
  return <>
    <textarea aria-label="Terminal input" />
    <button onClick={() => setOpen(true)}>Incoming request</button>
    <TakeoverDialog open={open} requesterName="Bob" seconds={7} pending={false} onDecide={() => setOpen(false)} />
  </>
}

it('returns focus to the interrupted terminal after denying control', async () => {
  render(<InterruptedController />)
  const interrupted = screen.getByRole('textbox', { name: 'Terminal input' })
  interrupted.focus()
  fireEvent.click(screen.getByRole('button', { name: 'Incoming request' }))
  const deny = screen.getByRole('button', { name: 'Deny' })
  expect(document.activeElement).toBe(deny)
  fireEvent.click(deny)
  await waitFor(() => expect(document.activeElement).toBe(interrupted))
})
