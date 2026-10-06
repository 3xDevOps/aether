import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
// The direct API, which sets itself up against the real clock: adding fake
// timers to this file would hang the tests that use it.
import userEvent from '@testing-library/user-event'
import { Dock } from '@/components/dock'

import { AppShell } from '@/components/shell/app-shell'
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
    runViewMemory: {},
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
      local: ['link.status', 'daemon.status', 'pull', 'update.check'],
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

describe('run view switch', () => {
  function openRun() {
    useStore.setState({ route: { name: 'run', params: { runId: active.id } } })
    render(<AppShell />)
    return screen.getByRole('tablist', { name: 'Run views' })
  }

  it('moves focus with the arrow keys, wrapping at both ends, without switching', async () => {
    const strip = openRun()
    const tabs = within(strip).getAllByRole('tab')
    const last = tabs.at(-1)!
    const terminal = within(strip).getByRole('tab', { name: 'Terminal' })

    terminal.focus()
    await userEvent.keyboard('{End}')
    await waitFor(() => expect(document.activeElement).toBe(last))
    await userEvent.keyboard('{ArrowRight}')
    await waitFor(() => expect(document.activeElement).toBe(tabs[0]))
    await userEvent.keyboard('{ArrowLeft}')
    await waitFor(() => expect(document.activeElement).toBe(last))

    // Arrowing past a view must not open it: Changes and Browser fetch on mount.
    expect(useStore.getState().route.params.view).toBeUndefined()
    fireEvent.mouseDown(within(strip).getByRole('tab', { name: 'Changes' }))
    expect(useStore.getState().route.params).toEqual({ runId: active.id, view: 'changes' })
  })

  it.each(['{Enter}', ' '])('opens the focused view on %j and keeps focus on its tab', async (key) => {
    const strip = openRun()
    const session = within(strip).getByRole('tab', { name: 'Session' })
    session.focus()
    await userEvent.keyboard(key)

    expect(useStore.getState().route.params.view).toBe('session')
    expect(document.activeElement).toBe(within(screen.getByRole('tablist', { name: 'Run views' })).getByRole('tab', { name: 'Session' }))
  })

  it('is one tab stop that lands on the open view', async () => {
    const strip = openRun()
    expect(within(strip).getAllByRole('tab').filter((tab) => tab.tabIndex === 0)).toEqual([])
    strip.focus()
    await waitFor(() => expect(document.activeElement).toBe(within(strip).getByRole('tab', { name: 'Terminal' })))
  })

  it('cycles views with [ and ] and toggles details with Ctrl+.', () => {
    openRun()
    fireEvent.keyDown(window, { key: ']' })
    expect(useStore.getState().route.params.view).toBe('changes')
    fireEvent.keyDown(window, { key: '[' })
    fireEvent.keyDown(window, { key: '[' })
    expect(useStore.getState().route.params.view).toBe('session')

    expect(screen.queryByRole('dialog', { name: 'Run details' })).toBeNull()
    fireEvent.keyDown(window, { key: '.', ctrlKey: true })
    expect(screen.getByRole('dialog', { name: 'Run details' })).toBeDefined()
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
    const handle = screen.getByRole('separator', { name: 'Resize terminal' })

    expect(handle.tabIndex).toBe(0)
    expect(handle.getAttribute('aria-controls')).toBe(
      screen.getByRole('region', { name: 'Environment terminal' }).id,
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
const collapseButton = () => screen.getByRole('button', { name: 'Hide sidebar' })

describe('sidebar resizer', () => {
  it('resizes from the keyboard, up to the bounds it announces', () => {
    render(<AppShell />)
    const handle = screen.getByRole('separator', { name: 'Resize sidebar' })
    expect(handle.tabIndex).toBe(0)
    expect(handle.getAttribute('aria-controls')).toBe('sidebar')

    const start = Number(handle.getAttribute('aria-valuenow'))
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
    ['the hide button', () => fireEvent.click(collapseButton())],
  ])('hands focus on when the sidebar is collapsed from %s', (_, collapse) => {
    render(<AppShell />)

    collapse()
    const expand = screen.getByRole('button', { name: 'Open sidebar' })
    expect(document.activeElement).toBe(expand)

    fireEvent.click(expand)
    expect(document.activeElement).toBe(collapseButton())
  })
})
