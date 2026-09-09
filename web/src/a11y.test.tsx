// The focus and keyboard contract, swept surface by surface. The expected
// classes are written out rather than read from `focusRing`, because a token
// compared against itself passes for any value, including one that suppresses
// the outline and puts nothing in its place.

import { fireEvent, render, screen, within } from '@testing-library/react'
// The direct API, which sets itself up against the real clock: adding fake
// timers to this file would hang the tests that use it.
import userEvent from '@testing-library/user-event'
import { Dock } from '@/components/dock'
import { CommandPalette } from '@/components/palette'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { PaletteDialogs } from '@/components/palette/dialogs'
import { AppShell } from '@/components/shell/app-shell'
import { RunTabs } from '@/routes/terminal/tabs'
import '@/routes'
import { lookupRoute } from '@/routes'
import { registeredRoutes } from '@/routes/registry'
import { useStore } from '@/store'
import { paletteDialogs } from '@/store/palette'
import { hydrate } from '@/store/sync'
import { openingTags, sourceFiles, type Tag } from '@/test/sources'
import { alice, approval, fakeApi, run, workspace } from '@/test/fixtures'
import { toRecord } from '@/store/runs'

const ring = ['focus-visible:outline-2', 'focus-visible:outline-ring']
/** A row that fills a scroll container draws the same outline inside. */
const offsets = ['focus-visible:outline-offset-2', 'focus-visible:-outline-offset-2']

/** Every role a keyboard can land on and this app actually renders. `option`
 * is not one: a native `<option>` is not tabbable, and cmdk keeps focus on its
 * input rather than moving it to the item. */
const controlRoles = [
  'button',
  'tab',
  'separator',
  'combobox',
  'textbox',
  'spinbutton',
  'checkbox',
  'switch',
  'menuitem',
  'menuitemcheckbox',
  'link',
  'tabpanel',
]

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

function expectFocusRing(root: HTMLElement) {
  // xterm's own hidden input is not ours to style: the terminal draws its
  // cursor, and a focus outline on an invisible textarea shows nothing.
  const found = controlRoles
    .flatMap((role) => within(root).queryAllByRole(role))
    .filter((el) => !el.closest('.xterm'))
    // A panel is only a control where it was given a tab stop; the ones whose
    // content is focusable are not something a keyboard lands on.
    .filter((el) => el.getAttribute('role') !== 'tabpanel' || el.hasAttribute('tabindex'))
  for (const el of found) {
    const name = el.getAttribute('aria-label') ?? el.textContent ?? el.outerHTML
    expect(ring.filter((c) => !el.classList.contains(c)), name).toEqual([])
    expect(offsets.some((c) => el.classList.contains(c)), name).toBe(true)
  }
  return found.length
}

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
    // A desktop gateway, so the routes that are a placeholder on a remote one
    // render their real bodies and the sweep can see their controls.
    capabilities: {
      gateway: 'local',
      methods: ['*'],
      ws: ['events', 'attach', 'terminal'],
      local: ['link.status', 'daemon.status', 'repo.sync', 'pull', 'update.check'],
    },
    inbox: { [workspace.id]: [approval()] },
    // Part way through the wizard, so its step chips are the buttons they
    // become once a step has been reached.
    onboardingStep: 'Workspace',
    onboardingFurthest: 'Workspace',
    feed: [
      {
        id: 'evt_1',
        seq: 1,
        time: '2026-08-14T10:03:00Z',
        workspace_id: workspace.id,
        run_id: active.id,
        actor_id: alice.id,
        type: 'workspace.timeline',
        payload: { kind: 'pause' },
      },
    ],
  })
})

describe('focus ring', () => {
  it('is on every control of the shell', () => {
    const { container } = render(<AppShell />)
    expect(expectFocusRing(container)).toBeGreaterThan(0)
  })

  // Driven off the registry rather than a hand-kept list, so a view added
  // later is swept without anyone remembering to add it here.
  it.each(registeredRoutes())('is on every control of the %s view', (name) => {
    const View = lookupRoute(name)
    if (!View) throw new Error(`no ${name} route registered`)
    const { container } = render(
      <View params={{ runId: active.id, workspaceId: workspace.id }} />,
    )
    // A view that renders no control at all is not evidence of anything.
    expect(expectFocusRing(container)).toBeGreaterThan(0)
  })

  it.each(paletteDialogs)(
    'is on every control of the %s form',
    (dialog) => {
      useStore.setState({ paletteDialog: dialog, paletteRunID: active.id })
      render(<PaletteDialogs />)
      expect(expectFocusRing(screen.getByRole('dialog'))).toBeGreaterThan(0)
    },
  )

  it('is on every control of the command palette', () => {
    useStore.setState({ paletteOpen: true })
    render(<CommandPalette />)
    expect(expectFocusRing(screen.getByRole('dialog'))).toBeGreaterThan(0)
  })

  // Menu items take real DOM focus under Radix's roving tabindex, so they are
  // controls the sweep has to see, and no route renders one open.
  it('is on every item of an open menu', () => {
    render(
      <DropdownMenu defaultOpen>
        <DropdownMenuTrigger>More</DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Kill run</DropdownMenuItem>
          <DropdownMenuItem>Delete run</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    )
    expect(expectFocusRing(screen.getByRole('menu'))).toBeGreaterThan(0)
  })

  it('is on the run tab strip', () => {
    const { container } = render(<RunTabs runID={active.id} active="terminal" />)
    expect(expectFocusRing(container)).toBeGreaterThan(0)
  })

  it('is on the dock strip, its panel and its resize handle', () => {
    const { container } = render(
      <Dock
        tabs={[{ id: 'a', label: 'Shell' }]}
        activeTab="a"
        onSelectTab={() => {}}
        onAddTab={() => {}}
        maxTabs={4}
        onCloseTab={() => {}}
        height={240}
        onHeightChange={() => {}}
        collapsed={false}
        onToggleCollapse={() => {}}
      >
        <div />
      </Dock>,
    )
    expect(expectFocusRing(container)).toBeGreaterThan(0)
  })
})

/**
 * Every hand-written control in one file. `tabIndex={0}` is in here too,
 * because a tab stop is a control whatever tag carries it.
 */
function controls(source: string): Tag[] {
  const tags = openingTags(source, [
    'input',
    'select',
    'textarea',
    'button',
    'summary',
    'a',
  ])
  for (const match of source.matchAll(/tabIndex=\{0\}|contentEditable/g)) {
    const start = source.lastIndexOf('<', match.index)
    tags.push({ name: 'tab stop', attributes: source.slice(start, match.index + 200) })
  }
  return tags
}

/** The files this sweep reads: every source the app ships, minus the one that
 * defines the token. */
async function ringedSources(): Promise<string[]> {
  const sources = (await sourceFiles()).filter((path) => !path.endsWith('/lib/utils.ts'))
  expect(sources.length).toBeGreaterThan(50)
  return sources
}

describe('the ring is the only ring', () => {
  // The sweep below can only reach surfaces a test renders. This reaches every
  // file, and is what keeps the guide's "one ring, one source" claim true.
  it('is written in exactly one place', async () => {
    const { readFile } = await import('node:fs/promises')
    const sources = await ringedSources()

    // The width, colour and style of the outline. An offset or an opacity
    // under the same variant is a modifier of the one indicator, not a second.
    const second = /focus(-visible)?:(ring-|outline-(2|4|8|none|hidden|ring|\[))/
    // The stylesheet cannot add a second indicator, but one rule there can
    // take this one away everywhere at once.
    const suppressed = /:focus[^{]*\{[^}]*outline\s*:\s*(none|0)/
    const offenders: string[] = []
    for (const path of sources) {
      const source = await readFile(path, 'utf8')
      const rule = path.endsWith('.css') ? suppressed : second
      if (rule.test(source)) offenders.push(path)
    }
    expect(offenders).toEqual([])

    // And the one place that does write it writes it once: a second, weaker
    // ring would hide here as readily as anywhere else.
    const token = await readFile(`${process.cwd()}/src/lib/utils.ts`, 'utf8')
    expect(token.match(/focus-visible:/g)).toHaveLength(3)
  })

  // The sweep above can only see a control a test renders. This one sees
  // every file, which is what catches a surface quietly losing the outline
  // while every rendered surface still has it.
  it('reaches every control drawn by hand, wherever it is', async () => {
    const { readFile } = await import('node:fs/promises')
    const bare: string[] = []
    for (const path of await ringedSources()) {
      for (const tag of controls(await readFile(path, 'utf8'))) {
        // A field may wear the outline through the shared `field` style;
        // nothing else can, so nothing else is let off with it. The Button
        // primitive counts for neither: a file that renders it can still
        // hand-roll a raw control beside it, which is how most of these lost
        // the outline in the first place.
        // `buttonVariants` is the Button primitive's own cva, which composes
        // the token; nothing else in the app reaches for it.
        const allowed = /^(input|select|textarea)$/.test(tag.name)
          ? /\b(focusRing|field)\b/
          : /\b(focusRing|buttonVariants)\b/
        if (!allowed.test(tag.attributes)) bare.push(`${path}: <${tag.name}>`)
      }
    }
    expect(bare).toEqual([])
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

  it('is a real button, which is what makes Enter and Space work', () => {
    render(<RunTabs runID={active.id} active="terminal" />)
    for (const tab of screen.getAllByRole('tab')) expect(tab.tagName).toBe('BUTTON')
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

describe('reveal flash', () => {
  it('is dropped for a reader who asked for less motion', () => {
    const { container } = render(<AppShell />)
    const flash = container.querySelector('[aria-hidden].pointer-events-none')
    expect(flash?.classList.contains('motion-reduce:hidden')).toBe(true)
  })
})
