import { fireEvent, render, screen } from '@testing-library/react'
import { createPortal } from 'react-dom'
import { useFileDrop, useRefuseStrayFileDrops } from '@/lib/file-drag'

afterEach(() => {
  delete (window as Window & { aetherDesktop?: { platform: string } }).aetherDesktop
})

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

describe('useFileDrop', () => {
  function Target({ onFiles }: { onFiles: (files: File[]) => void }) {
    const drop = useFileDrop({ onFiles, limit: 8 })
    return (
      <div data-testid="target" data-count={drop.count ?? undefined} data-refused={drop.refused || undefined} {...drop.handlers}>
        {createPortal(<p>Dialog</p>, document.body)}
      </div>
    )
  }
  const files = { types: ['Files'], files: [new File(['x'], 'shot.png')], items: [{ kind: 'file' }] }

  it('takes a drop on the target but not on a dialog it renders through a portal', () => {
    const onFiles = vi.fn()
    render(<Target onFiles={onFiles} />)
    fireEvent.drop(screen.getByText('Dialog'), { dataTransfer: files })
    expect(onFiles).not.toHaveBeenCalled()
    fireEvent.drop(screen.getByTestId('target'), { dataTransfer: files })
    expect(onFiles).toHaveBeenCalledWith(files.files)
  })

  it('takes a native file item when the webview omits the Files type', () => {
    Object.assign(window, { aetherDesktop: { platform: 'linux' } })
    const onFiles = vi.fn()
    const file = new File(['x'], 'shot.png', { type: 'image/png' })
    render(<Target onFiles={onFiles} />)
    fireEvent.drop(screen.getByTestId('target'), { dataTransfer: { types: [], files: [], items: [{ kind: 'file', getAsFile: () => file }] } })
    expect(onFiles).toHaveBeenCalledWith([file])
  })

  it.each(['darwin', 'win32'])('leaves a %s webview file item without the Files type alone', (platform) => {
    Object.assign(window, { aetherDesktop: { platform } })
    const onFiles = vi.fn()
    const file = new File(['x'], 'shot.png', { type: 'image/png' })
    render(<Target onFiles={onFiles} />)
    fireEvent.drop(screen.getByTestId('target'), { dataTransfer: { types: [], files: [], items: [{ kind: 'file', getAsFile: () => file }] } })
    expect(onFiles).not.toHaveBeenCalled()
  })

  it('counts files from Linux transfers that expose files but no items', () => {
    Object.assign(window, { aetherDesktop: { platform: 'linux' } })
    const onFiles = vi.fn()
    const manyFiles = { types: [], files: Array.from({ length: 9 }, (_, index) => new File(['x'], `${index}.png`)), items: [] }
    render(<Target onFiles={onFiles} />)
    const target = screen.getByTestId('target')
    fireEvent.dragEnter(target, { dataTransfer: manyFiles })
    expect(target.getAttribute('data-count')).toBe('9')
    expect(target.getAttribute('data-refused')).toBe('true')
    fireEvent.drop(target, { dataTransfer: manyFiles })
    expect(onFiles).not.toHaveBeenCalled()
  })
})
