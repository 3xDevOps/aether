import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ShortcutsDialog } from '@/components/shortcuts'
import { useStore } from '@/store'

beforeEach(() => {
  useStore.setState({ shortcutsOpen: false, singleKeyShortcuts: true })
})

describe('shortcut reference', () => {
  it('opens on Shift+/ and from the store, and Escape closes it', async () => {
    render(<ShortcutsDialog />)

    fireEvent.keyDown(window, { key: '?', shiftKey: true })
    expect(await screen.findByRole('heading', { name: 'Keyboard shortcuts' })).toBeDefined()
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('heading', { name: 'Keyboard shortcuts' })).toBeNull()
    expect(useStore.getState().shortcutsOpen).toBe(false)

    act(() => useStore.getState().setShortcutsOpen(true))
    expect(await screen.findByRole('heading', { name: 'Keyboard shortcuts' })).toBeDefined()
  })

  // Only the reference is mounted, so of the global keys only its own answers.
  it("lists the keys that answer here and a run's keys, by scope", async () => {
    render(<ShortcutsDialog />)
    act(() => useStore.getState().setShortcutsOpen(true))

    expect(await screen.findByRole('heading', { name: 'Everywhere' })).toBeDefined()
    expect(screen.getByText('Open this reference')).toBeDefined()
    expect(screen.queryByLabelText('g then b')).toBeNull()
    expect(screen.getByRole('heading', { name: 'In a run' })).toBeDefined()
    expect(screen.getByText('Leave a run for the board')).toBeDefined()
  })

  it('ignores a "?" typed into a field', () => {
    render(
      <>
        <ShortcutsDialog />
        <input aria-label="task" />
      </>,
    )

    fireEvent.keyDown(screen.getByLabelText('task'), { key: '?', shiftKey: true })

    expect(screen.queryByRole('heading', { name: 'Keyboard shortcuts' })).toBeNull()
  })

  // Overlay ownership is read from the event, not the store.
  it.each(['dialog', 'menu'])('yields to an open %s', (role) => {
    render(<ShortcutsDialog />)
    const overlay = document.createElement('div')
    overlay.setAttribute('role', role)
    document.body.append(overlay)
    onTestFinished(() => overlay.remove())

    fireEvent.keyDown(overlay, { key: '?', shiftKey: true })

    expect(screen.queryByRole('heading', { name: 'Keyboard shortcuts' })).toBeNull()
  })
})
