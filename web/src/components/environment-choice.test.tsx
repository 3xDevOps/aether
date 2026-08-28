import { fireEvent, render, screen } from '@testing-library/react'
import { EnvironmentChoice } from '@/components/environment-choice'
import { useStore } from '@/store'
import { serverInfo } from '@/test/fixtures'

// The component reads the standard ref from the server-info slice, exactly
// where hydration puts it; each test seeds the store it needs.
function seed(info = serverInfo) {
  useStore.setState({ info })
}

describe('environment choice', () => {
  it('preselects the standard environment and emits its pinned ref', () => {
    seed()
    const onChange = vi.fn()
    render(<EnvironmentChoice onChange={onChange} />)

    const standard = screen.getByRole('radio', { name: /Standard environment/ })
    expect(standard).toHaveProperty('checked', true)
    expect(onChange).toHaveBeenLastCalledWith({
      custom_image: serverInfo.standard_image,
    })
    // The pinned ref is visible but small, not part of the pitch.
    expect(screen.getByText(serverInfo.standard_image as string)).toBeDefined()
  })

  it('emits the exact payload for each card', () => {
    seed()
    const onChange = vi.fn()
    render(<EnvironmentChoice onChange={onChange} />)

    fireEvent.click(screen.getByRole('radio', { name: /Minimal starter/ }))
    expect(onChange).toHaveBeenLastCalledWith({ neutral_image: true })

    fireEvent.click(screen.getByRole('radio', { name: /Custom image/ }))
    fireEvent.change(screen.getByLabelText('Image reference'), {
      target: { value: ' registry.example/dev:1 ' },
    })
    expect(onChange).toHaveBeenLastCalledWith({
      custom_image: 'registry.example/dev:1',
    })

    fireEvent.click(screen.getByRole('radio', { name: /Standard environment/ }))
    expect(onChange).toHaveBeenLastCalledWith({
      custom_image: serverInfo.standard_image,
    })
  })

  it('reveals the image input only for the custom card, and emits null until a ref is typed', () => {
    seed()
    const onChange = vi.fn()
    render(<EnvironmentChoice onChange={onChange} />)

    expect(screen.queryByLabelText('Image reference')).toBeNull()

    fireEvent.click(screen.getByRole('radio', { name: /Custom image/ }))
    expect(screen.getByLabelText('Image reference')).toBeDefined()
    // No ref typed yet: the choice is incomplete, callers keep submit off.
    expect(onChange).toHaveBeenLastCalledWith(null)
  })

  it('hides the standard card and preselects the starter on an older server', () => {
    seed({ ...serverInfo, standard_image: undefined })
    const onChange = vi.fn()
    render(<EnvironmentChoice onChange={onChange} />)

    expect(
      screen.queryByRole('radio', { name: /Standard environment/ }),
    ).toBeNull()
    expect(
      screen.getByRole('radio', { name: /Minimal starter/ }),
    ).toHaveProperty('checked', true)
    expect(onChange).toHaveBeenLastCalledWith({ neutral_image: true })
  })
})
