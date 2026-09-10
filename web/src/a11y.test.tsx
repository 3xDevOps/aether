import { fireEvent, render, screen } from '@testing-library/react'
// The direct API, which sets itself up against the real clock: adding fake
// timers to this file would hang the tests that use it.
import userEvent from '@testing-library/user-event'
import { Dock } from '@/components/dock'

import { AppShell } from '@/components/shell/app-shell'
import { RunTabs } from '@/routes/terminal/tabs'
import { useStore } from '@/store'
import { hydrate } from '@/store/sync'
import { fakeApi, run } from '@/test/fixtures'
import { toRecord } from '@/store/runs'

// jsdom has neither of the two browser APIs xterm and the dialogs reach for.
Element.prototype.scrollIntoView = vi.fn()
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
)

const active = run({ id: 'run_1', task: 'rewrite the checkout flow' })
// One test swaps `navigate` for a spy, and the store outlives a single test.
const realNavigate = useStore.getState().navigate

beforeEach(async () => {
  useStore.setState({
    sidebarCollapsed: false,
    activeWorkspace: '',
    route: { name: 'overview', params: {} },
    paletteOpen: false,
    paletteDialog: null,
    paletteRunID: null,
    sidebarWidth: 280,
    navigate: realNavigate,
  })
  await hydrate(useStore, fakeApi())
  useStore.setState({
    runs: { ...useStore.getState().runs, [active.id]: toRecord(active) },
    // A desktop gateway keeps local routes rendering their full content.
    capabilities: {
      gateway: 'local',
      methods: ['*'],
      ws: ['events', 'attach', 'terminal'],
      local: ['link.status', 'daemon.status', 'repo.sync', 'pull', 'update.check'],
    },
  })
})

/** A control outside the strip, to hold the focus a leak would steal. */
function spare(): HTMLButtonElement {
  const button = document.createElement('button')
  document.body.append(button)
  onTestFinished(() => button.remove())
  return button
}

describe('run tab strip', () => {
  it('moves focus with the arrow keys, wrapping at both ends', () => {
    const navigate = vi.fn()
    useStore.setState({ navigate })
    render(<RunTabs runID={active.id} active="terminal" />)
    const tabs = screen.getAllByRole('tab')

    fireEvent.keyDown(tabs[1], { key: 'ArrowRight' })
    expect(document.activeElement).toBe(tabs[2])

    fireEvent.keyDown(tabs[2], { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(tabs[1])

    fireEvent.keyDown(tabs[1], { key: 'Home' })
    expect(document.activeElement).toBe(tabs[0])

    fireEvent.keyDown(tabs[0], { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(tabs[3])

    fireEvent.keyDown(tabs[3], { key: 'End' })
    expect(document.activeElement).toBe(tabs[3])

    // Arrowing past a tab must not open it: each one costs an attach socket
    // or a patch fetch to mount. A click on the same tab proves the strip was
    // wired up at all.
    expect(navigate).not.toHaveBeenCalled()
    fireEvent.click(tabs[3])
    expect(navigate).toHaveBeenCalledWith('events', { runId: active.id })
  })

  it.each(['{Enter}', '[Space]'])('opens the focused tab on %s', async (key) => {
    const navigate = vi.fn()
    useStore.setState({ navigate })
    render(<RunTabs runID={active.id} active="terminal" />)
    const tabs = screen.getAllByRole('tab')
    tabs[1].focus()

    fireEvent.keyDown(tabs[1], { key: 'ArrowRight' })
    await userEvent.keyboard(key)

    expect(navigate).toHaveBeenCalledWith('diff', { runId: active.id })
  })

  it.each(['{Enter}', '[Space]'])(
    'carries focus into the strip the next route draws, on %s',
    async (key) => {
      useStore.setState({ route: { name: 'run', params: { runId: active.id } } })
      render(<AppShell />)
      screen.getByRole('tab', { name: 'Overview' }).focus()

      fireEvent.keyDown(document.activeElement as HTMLElement, { key: 'ArrowLeft' })
      await userEvent.keyboard(key)

      expect(useStore.getState().route.name).toBe('events')
      expect(document.activeElement?.textContent).toBe('Events')
    },
  )

  // A handoff armed by anything short of a real activation is never consumed,
  // and it is module scope, so it survives the strip unmounting and fires on
  // the next one, stealing focus from whatever the reader clicked.
  it('does not take focus back after a pointer click', async () => {
    const navigate = vi.fn()
    useStore.setState({ navigate })
    const { unmount } = render(<RunTabs runID={active.id} active="terminal" />)
    const elsewhere = spare()

    // A real click rather than a fabricated `detail`: the browser's own value
    // is what the guard reads, and only user-event produces it.
    await userEvent.click(screen.getByRole('tab', { name: 'Diff' }))
    unmount()
    elsewhere.focus()
    render(<RunTabs runID={active.id} active="diff" />)

    expect(document.activeElement).toBe(elsewhere)
  })

  it('does not take focus back after reopening the tab already open', () => {
    const navigate = vi.fn()
    useStore.setState({ navigate })
    const { unmount } = render(<RunTabs runID={active.id} active="terminal" />)
    const elsewhere = spare()

    fireEvent.click(screen.getByRole('tab', { name: 'Terminal' }))
    unmount()
    elsewhere.focus()
    render(<RunTabs runID={active.id} active="terminal" />)

    expect(document.activeElement).toBe(elsewhere)
  })

  it('does not take focus back after a Space that was abandoned', async () => {
    const navigate = vi.fn()
    useStore.setState({ navigate })
    const { unmount } = render(<RunTabs runID={active.id} active="terminal" />)
    const elsewhere = spare()

    // Held, then focus moves, which is how the browser cancels the click a
    // Space would otherwise fire on release.
    screen.getByRole('tab', { name: 'Diff' }).focus()
    await userEvent.keyboard('[Space>]')
    elsewhere.focus()
    await userEvent.keyboard('[/Space]')
    expect(navigate).not.toHaveBeenCalled()

    unmount()
    render(<RunTabs runID={active.id} active="diff" />)
    expect(document.activeElement).toBe(elsewhere)
  })

  it('keeps one tab stop, on the tab focus is on', () => {
    const { rerender } = render(<RunTabs runID={active.id} active="terminal" />)
    const tabs = screen.getAllByRole('tab')
    expect(tabs.filter((t) => t.tabIndex === 0)).toEqual([tabs[1]])

    fireEvent.keyDown(tabs[1], { key: 'End' })
    expect(tabs.filter((t) => t.tabIndex === 0)).toEqual([tabs[3]])

    // A run-to-run switch reuses this strip, so the stop has to come back.
    rerender(<RunTabs runID="run_2" active="terminal" />)
    expect(tabs.filter((t) => t.tabIndex === 0)).toEqual([tabs[1]])
  })

  it('leaves a modifier chord to the browser', () => {
    render(<RunTabs runID={active.id} active="terminal" />)
    const tabs = screen.getAllByRole('tab')
    tabs[1].focus()

    fireEvent.keyDown(tabs[1], { key: 'ArrowLeft', altKey: true })

    expect(document.activeElement).toBe(tabs[1])
  })

})

describe('dock', () => {
  function dock(over: Partial<React.ComponentProps<typeof Dock>> = {}) {
    const props = {
      tabs: [
        { id: 'a', label: 'Shell' },
        { id: 'b', label: 'Logs' },
        { id: 'c', label: 'Build' },
      ],
      activeTab: 'b',
      onSelectTab: vi.fn(),
      maxTabs: 4,
      height: 240,
      onHeightChange: vi.fn(),
      collapsed: false,
      onToggleCollapse: vi.fn(),
      children: <div>terminal</div>,
      ...over,
    }
    render(<Dock {...props} />)
    return props
  }

  it('moves focus with the arrow keys and selects on click', () => {
    const { onSelectTab } = dock()
    const tabs = screen.getAllByRole('tab')

    fireEvent.keyDown(tabs[1], { key: 'ArrowRight' })
    expect(document.activeElement).toBe(tabs[2])
    fireEvent.keyDown(tabs[2], { key: 'Home' })
    expect(document.activeElement).toBe(tabs[0])
    // Switching a dock tab remounts an xterm host, so an arrow key alone
    // must not do it.
    expect(onSelectTab).not.toHaveBeenCalled()

    fireEvent.click(tabs[0])
    expect(onSelectTab).toHaveBeenCalledWith('a')
  })

  it('opens the focused tab on Enter', async () => {
    const { onSelectTab } = dock()
    screen.getAllByRole('tab')[1].focus()

    fireEvent.keyDown(screen.getAllByRole('tab')[1], { key: 'ArrowRight' })
    await userEvent.keyboard('{Enter}')

    expect(onSelectTab).toHaveBeenCalledWith('c')
  })

  it('keeps the tab stop where focus is, however focus got there', () => {
    dock()
    const tabs = screen.getAllByRole('tab')
    fireEvent.keyDown(tabs[1], { key: 'End' })
    expect(tabs.filter((t) => t.tabIndex === 0)).toEqual([tabs[2]])

    fireEvent.focus(tabs[0])
    expect(tabs.filter((t) => t.tabIndex === 0)).toEqual([tabs[0]])
  })

  it('names the panel from its selected tab and from no other', () => {
    dock()
    const panel = screen.getByRole('tabpanel')
    const selected = screen.getByRole('tab', { selected: true })
    expect(panel.getAttribute('aria-labelledby')).toBe(selected.id)
    expect(selected.getAttribute('aria-controls')).toBe(panel.id)

    for (const tab of screen.getAllByRole('tab', { selected: false })) {
      expect(tab.getAttribute('aria-controls')).toBeNull()
    }
  })

  it('closes removable tabs by pointer or key and repairs focus', () => {
    const onSelectTab = vi.fn()
    const onCloseTab = vi.fn()
    const props = {
      tabs: [
        { id: 'main', label: 'Main', permanent: true },
        { id: 'logs', label: 'Logs' },
        { id: 'build', label: 'Build' },
      ],
      activeTab: 'main',
      onSelectTab,
      onCloseTab,
      maxTabs: 4,
      height: 240,
      onHeightChange: vi.fn(),
      collapsed: false,
      onToggleCollapse: vi.fn(),
      children: <div>terminal</div>,
    }
    const { rerender } = render(<Dock {...props} />)
    const main = screen.getByRole('tab', { name: 'Main' })
    main.focus()
    const logs = screen.getByRole('tab', { name: 'Logs' })
    const closeLogs = logs.querySelector('[data-tab-close]')
    expect(closeLogs).not.toBeNull()
    fireEvent.pointerDown(closeLogs!)
    expect(onCloseTab).not.toHaveBeenCalled()
    fireEvent.click(closeLogs!)

    expect(onCloseTab).toHaveBeenCalledWith('logs')
    expect(onSelectTab).not.toHaveBeenCalled()
    expect(logs.getAttribute('aria-keyshortcuts')).toBe('Delete Backspace')
    expect(main.querySelector('[data-tab-close]')).toBeNull()

    rerender(<Dock {...props} tabs={[props.tabs[0], props.tabs[2]]} />)
    const build = screen.getByRole('tab', { name: 'Build' })
    // Closing an inactive tab must not steal focus from the selected tab.
    expect(document.activeElement).toBe(main)

    build.focus()
    fireEvent.keyDown(build, { key: 'Delete' })
    expect(onCloseTab).toHaveBeenLastCalledWith('build')
    rerender(<Dock {...props} tabs={[props.tabs[0]]} />)
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Main' }))

    fireEvent.keyDown(screen.getByRole('tab', { name: 'Main' }), { key: 'Backspace' })
    expect(onCloseTab).toHaveBeenCalledTimes(2)
  })

  it('moves focus to Add terminal tab after the last tab closes', () => {
    const onCloseTab = vi.fn()
    const props = {
      tabs: [{ id: 'shell', label: 'Shell' }],
      activeTab: 'shell',
      onSelectTab: vi.fn(),
      onAddTab: vi.fn(),
      onCloseTab,
      maxTabs: 4,
      height: 240,
      onHeightChange: vi.fn(),
      collapsed: false,
      onToggleCollapse: vi.fn(),
      children: <div>terminal</div>,
    }
    const { rerender } = render(<Dock {...props} />)
    const shell = screen.getByRole('tab', { name: 'Shell' })
    shell.focus()
    fireEvent.keyDown(shell, { key: 'Backspace' })
    expect(onCloseTab).toHaveBeenCalledWith('shell')

    rerender(<Dock {...props} tabs={[]} activeTab="" />)
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Add terminal tab' }),
    )
  })

  it('does not steal focus after a delayed close when focus moved elsewhere', () => {
    const onCloseTab = vi.fn()
    const props = {
      tabs: [
        { id: 'a', label: 'Shell' },
        { id: 'b', label: 'Logs' },
      ],
      activeTab: 'a',
      onSelectTab: vi.fn(),
      onCloseTab,
      maxTabs: 4,
      height: 240,
      onHeightChange: vi.fn(),
      collapsed: false,
      onToggleCollapse: vi.fn(),
      children: <div>terminal</div>,
    }
    const { rerender } = render(<Dock {...props} />)
    const closing = screen.getByRole('tab', { name: 'Logs' })
    closing.focus()
    fireEvent.keyDown(closing, { key: 'Delete' })

    const elsewhere = spare()
    elsewhere.focus()
    rerender(<Dock {...props} tabs={[props.tabs[0]]} />)

    expect(document.activeElement).toBe(elsewhere)
  })
  it('cancels delayed close after external focus then body focus', () => {
    const onCloseTab = vi.fn()
    const props = {
      tabs: [
        { id: 'a', label: 'Shell' },
        { id: 'b', label: 'Logs' },
      ],
      activeTab: 'a',
      onSelectTab: vi.fn(),
      onCloseTab,
      maxTabs: 4,
      height: 240,
      onHeightChange: vi.fn(),
      collapsed: false,
      onToggleCollapse: vi.fn(),
      children: <div>terminal</div>,
    }
    const { rerender } = render(<Dock {...props} />)
    const closing = screen.getByRole('tab', { name: 'Logs' })
    closing.focus()
    fireEvent.keyDown(closing, { key: 'Delete' })

    const elsewhere = spare()
    elsewhere.focus()
    fireEvent.focusIn(elsewhere)
    const body = document.body
    const priorTabIndex = body.getAttribute('tabindex')
    body.tabIndex = -1
    try {
      body.focus()
      fireEvent.focusIn(body)
      expect(document.activeElement).toBe(body)

      rerender(<Dock {...props} tabs={[props.tabs[0]]} />)
      expect(document.activeElement).toBe(body)
    } finally {
      if (priorTabIndex === null) body.removeAttribute('tabindex')
      else body.setAttribute('tabindex', priorTabIndex)
    }
  })


  it('keeps the tab stop on the selected tab when another closes', () => {
    const { rerender } = render(
      <Dock
        tabs={[
          { id: 'a', label: 'Shell' },
          { id: 'b', label: 'Logs' },
          { id: 'c', label: 'Build' },
        ]}
        activeTab="a"
        onSelectTab={vi.fn()}
        maxTabs={4}
        height={240}
        onHeightChange={vi.fn()}
        collapsed={false}
        onToggleCollapse={vi.fn()}
      >
        <div>terminal</div>
      </Dock>,
    )
    fireEvent.keyDown(screen.getAllByRole('tab')[0], { key: 'End' })

    rerender(
      <Dock
        tabs={[
          { id: 'a', label: 'Shell' },
          { id: 'b', label: 'Logs' },
        ]}
        activeTab="a"
        onSelectTab={vi.fn()}
        maxTabs={4}
        height={240}
        onHeightChange={vi.fn()}
        collapsed={false}
        onToggleCollapse={vi.fn()}
      >
        <div>terminal</div>
      </Dock>,
    )

    const tabs = screen.getAllByRole('tab')
    expect(tabs.filter((t) => t.tabIndex === 0)).toEqual([tabs[0]])
  })

  it('leaves no tab list and no panel behind while it holds no tabs', () => {
    dock({ tabs: [], activeTab: '' })
    expect(screen.queryByRole('tablist')).toBeNull()
    expect(screen.queryByRole('tabpanel')).toBeNull()
  })

  it('points at no panel while it is shut', () => {
    dock({ collapsed: true })
    expect(screen.queryByRole('tabpanel')).toBeNull()
    for (const tab of screen.getAllByRole('tab')) {
      expect(tab.getAttribute('aria-controls')).toBeNull()
    }
  })

  it('resizes from the keyboard, up to its own bounds', () => {
    const { onHeightChange, onToggleCollapse } = dock()
    const handle = screen.getByRole('separator', { name: 'Resize terminal dock' })

    expect(handle.tabIndex).toBe(0)
    expect(handle.getAttribute('aria-controls')).toBe(
      screen.getByRole('region', { name: 'Terminal dock' }).id,
    )

    fireEvent.keyDown(handle, { key: 'ArrowUp' })
    expect(onHeightChange).toHaveBeenLastCalledWith(256)
    fireEvent.keyDown(handle, { key: 'ArrowDown' })
    expect(onHeightChange).toHaveBeenLastCalledWith(224)

    fireEvent.keyDown(handle, { key: 'Home' })
    expect(onHeightChange).toHaveBeenLastCalledWith(Number(handle.getAttribute('aria-valuemin')))
    fireEvent.keyDown(handle, { key: 'End' })
    expect(onHeightChange).toHaveBeenLastCalledWith(Number(handle.getAttribute('aria-valuemax')))

    fireEvent.keyDown(handle, { key: 'Enter' })
    expect(onToggleCollapse).toHaveBeenCalled()
  })
})

const splitter = () => screen.getByRole('separator', { name: 'Resize sidebar' })
const collapseButton = () => screen.getByRole('button', { name: 'Collapse sidebar' })

describe('sidebar resizer', () => {
  it('resizes from the keyboard, up to the bounds it announces', () => {
    render(<AppShell />)
    const handle = screen.getByRole('separator', { name: 'Resize sidebar' })
    expect(handle.tabIndex).toBe(0)
    expect(handle.getAttribute('aria-controls')).toBe('sidebar')

    const start = useStore.getState().sidebarWidth
    fireEvent.keyDown(handle, { key: 'ArrowRight' })
    expect(useStore.getState().sidebarWidth).toBe(start + 16)
    fireEvent.keyDown(handle, { key: 'ArrowLeft' })
    expect(useStore.getState().sidebarWidth).toBe(start)

    fireEvent.keyDown(handle, { key: 'Home' })
    expect(useStore.getState().sidebarWidth).toBe(
      Number(handle.getAttribute('aria-valuemin')),
    )
    fireEvent.keyDown(handle, { key: 'End' })
    expect(useStore.getState().sidebarWidth).toBe(
      Number(handle.getAttribute('aria-valuemax')),
    )
  })

  it.each([
    ['the splitter', () => fireEvent.keyDown(splitter(), { key: 'Enter' })],
    ['the collapse button', () => fireEvent.click(collapseButton())],
  ])('hands focus on when the sidebar is collapsed from %s', (_, collapse) => {
    render(<AppShell />)

    collapse()
    const expand = screen.getByRole('button', { name: 'Expand sidebar' })
    expect(document.activeElement).toBe(expand)

    fireEvent.click(expand)
    expect(document.activeElement).toBe(collapseButton())
  })
})
