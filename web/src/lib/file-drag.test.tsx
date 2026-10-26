import { fireEvent, render, screen } from '@testing-library/react'
import { useRefuseStrayFileDrops } from '@/lib/file-drag'

function Page() {
  useRefuseStrayFileDrops()
  return <p>Page</p>
}

function drag(type: 'dragover' | 'drop', types: string[]) {
  const event = new Event(type, { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown }
  event.dataTransfer = { types, dropEffect: 'copy' }
  window.dispatchEvent(event)
  return event
}

describe('useRefuseStrayFileDrops', () => {
  it('cancels a file drag that no target took, so the browser does not open the file', () => {
    render(<Page />)
    const over = drag('dragover', ['Files'])
    expect(over.defaultPrevented).toBe(true)
    expect((over.dataTransfer as DataTransfer).dropEffect).toBe('none')
    expect(drag('drop', ['Files']).defaultPrevented).toBe(true)
  })

  it('leaves text and link drags to the browser', () => {
    render(<Page />)
    expect(drag('drop', ['text/plain']).defaultPrevented).toBe(false)
  })

  it('stops refusing once the page is gone', () => {
    const { unmount } = render(<Page />)
    unmount()
    expect(drag('drop', ['Files']).defaultPrevented).toBe(false)
    expect(screen.queryByText('Page')).toBeNull()
    fireEvent.dragEnd(document.body)
  })
})
