import { fireEvent, render, screen } from '@testing-library/react'
import '@/components/shortcuts'
import { Slot } from '@/components/slots'
import { useStore } from '@/store'

beforeEach(() => {
  useStore.setState({
    paletteOpen: false,
    paletteDialog: null,
  })
})

describe('shortcut reference', () => {
  it('rides the status bar slot', () => {
    render(<Slot name="statusbar" />)

    expect(screen.getByTitle('Keyboard shortcuts')).toBeDefined()
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

  it('yields to a dialog already on screen', () => {
    useStore.setState({ paletteOpen: true })
    render(<Slot name="statusbar" />)

    fireEvent.keyDown(window, { key: '?', shiftKey: true })

    expect(
      screen.queryByRole('heading', { name: 'Keyboard shortcuts' }),
    ).toBeNull()
  })
})
