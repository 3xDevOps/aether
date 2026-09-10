import { fireEvent, render, screen } from '@testing-library/react'
import '@/components/shortcuts'
import { Slot } from '@/components/slots'
import { hintOn } from '@/test/tooltip'

describe('shortcut reference', () => {
  it('rides the status bar slot, and says so on focus', async () => {
    render(<Slot name="statusbar" />)

    const trigger = screen.getByRole('button', { name: 'Keyboard shortcuts' })

    expect(await hintOn(trigger)).toBe('Keyboard shortcuts')
  })

  it('opens on Shift+/ and from the trigger', async () => {
    render(<Slot name="statusbar" />)

    fireEvent.keyDown(window, { key: '?', shiftKey: true })

    expect(
      await screen.findByRole('heading', { name: 'Keyboard shortcuts' }),
    ).toBeDefined()
    // The table names the other global key.
    expect(screen.getByText('Open the command palette')).toBeDefined()
  })

  it('ignores a "?" typed into a field', () => {
    render(
      <>
        <Slot name="statusbar" />
        <input aria-label="task" />
      </>,
    )

    fireEvent.keyDown(screen.getByLabelText('task'), {
      key: '?',
      shiftKey: true,
    })

    expect(
      screen.queryByRole('heading', { name: 'Keyboard shortcuts' }),
    ).toBeNull()
  })

  // A key pressed inside an overlay belongs to that overlay, whatever the
  // store thinks is open: this asks the event, not the store.
  it.each(['dialog', 'menu'])('yields to an open %s', (role) => {
    render(<Slot name="statusbar" />)
    const overlay = document.createElement('div')
    overlay.setAttribute('role', role)
    document.body.append(overlay)
    onTestFinished(() => overlay.remove())

    fireEvent.keyDown(overlay, { key: '?', shiftKey: true })

    expect(
      screen.queryByRole('heading', { name: 'Keyboard shortcuts' }),
    ).toBeNull()
  })
})
