// The shell's single-key navigation, driven through the real shell. Where a
// test asserts that nothing happened, it presses the same key somewhere it
// does work first, so it cannot pass by the shortcut never having fired.

import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AppShell } from '@/components/shell/app-shell'
import { useStore } from '@/store'
import { hydrate } from '@/store/sync'
import { fakeApi, serverInfo, vera } from '@/test/fixtures'
import { hintOn } from '@/test/tooltip'
import '@/routes'

beforeEach(async () => {
  useStore.setState({
    sidebarCollapsed: false,
    activeWorkspace: '',
    route: { name: 'board', params: {} },
    paletteOpen: false,
    paletteDialog: null,
  })
  await hydrate(useStore, fakeApi())
})

function press(key: string, target: Element | Window = window, init = {}) {
  fireEvent.keyDown(target, { key, ...init })
}

/** Appends a node the guard has to notice, removed however the test ends. */
function overlay(html: string): HTMLElement {
  const host = document.createElement('div')
  host.innerHTML = html
  document.body.append(host)
  onTestFinished(() => host.remove())
  return host.firstElementChild as HTMLElement
}

describe('navigation shortcuts', () => {
  it('goes to the board and to all runs on the g chord', () => {
    render(<AppShell />)

    press('g')
    press('l')
    expect(useStore.getState().route.name).toBe('overview')

    press('g')
    press('b')
    expect(useStore.getState().route.name).toBe('board')
  })

  it('ignores the second key without the g prefix', () => {
    render(<AppShell />)

    press('l')
    expect(useStore.getState().route.name).toBe('board')

    press('g')
    press('l')
    expect(useStore.getState().route.name).toBe('overview')
  })

  it('forgets a g that was never completed in time', () => {
    vi.useFakeTimers()
    onTestFinished(() => {
      vi.useRealTimers()
    })
    render(<AppShell />)

    press('g')
    vi.advanceTimersByTime(2000)
    press('l')
    expect(useStore.getState().route.name).toBe('board')

    press('g')
    vi.advanceTimersByTime(500)
    press('l')
    expect(useStore.getState().route.name).toBe('overview')
  })

  it('holds the chord across a reach for a modifier', () => {
    useStore.setState({ route: { name: 'overview', params: {} } })
    render(<AppShell />)

    press('g')
    // Pressing Shift is its own keydown, and it is not the reader giving up
    // on the chord halfway through.
    press('Shift', window, { shiftKey: true })
    press('b')

    expect(useStore.getState().route.name).toBe('board')
  })

  it('forgets a g the moment a key goes somewhere else', () => {
    useStore.setState({ route: { name: 'overview', params: {} } })
    render(<AppShell />)
    const term = overlay('<div class="xterm"><span></span></div>')

    // The b reaches the agent, which also ends the chord the shell was
    // holding: a later b is a fresh key, not the tail of that g.
    press('g')
    press('b', term.firstElementChild as HTMLElement)
    press('b')
    expect(useStore.getState().route.name).toBe('overview')

    press('g')
    press('b')
    expect(useStore.getState().route.name).toBe('board')
  })

  it('opens the launch form on n, and not for a member who may not launch', () => {
    render(<AppShell />)
    press('n')
    expect(useStore.getState().paletteDialog).toBe('launch')

    act(() => {
      useStore.setState({ paletteDialog: null, info: { ...serverInfo, member: vera } })
    })
    press('n')
    expect(useStore.getState().paletteDialog).toBe(null)
  })

  it('leaves a run for the board on Escape, and does nothing elsewhere', () => {
    // The Overview tab rather than the Terminal one: same run route family,
    // no xterm to stand up for a keyboard assertion.
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1' } } })
    render(<AppShell />)

    press('Escape')
    expect(useStore.getState().route.name).toBe('board')

    useStore.setState({ route: { name: 'members', params: {} } })
    press('Escape')
    expect(useStore.getState().route.name).toBe('members')
  })

  it('lets a pending chord swallow the Escape that cancels it', () => {
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1' } } })
    render(<AppShell />)

    press('g')
    press('Escape', screen.getByRole('tab', { name: 'Overview' }))
    expect(useStore.getState().route.name).toBe('run')

    // The same Escape with no chord pending is the one that leaves.
    press('Escape', screen.getByRole('tab', { name: 'Overview' }))
    expect(useStore.getState().route.name).toBe('board')
  })

  // Radix dismisses an overlay from a capturing document listener and marks
  // the event handled. React has already closed the overlay by the time this
  // listener runs, so the only thing left to read is the event itself.
  it('leaves an Escape another layer already acted on alone', () => {
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1' } } })
    render(<AppShell />)

    const dismiss = (e: KeyboardEvent) => e.preventDefault()
    document.addEventListener('keydown', dismiss, { capture: true })

    // Dispatched at an element so the document listener is on the path,
    // exactly as a real key press reaches Radix before the window.
    const tab = screen.getByRole('tab', { name: 'Overview' })
    press('Escape', tab)
    expect(useStore.getState().route.name).toBe('run')

    document.removeEventListener('keydown', dismiss, { capture: true })
    press('Escape', tab)
    expect(useStore.getState().route.name).toBe('board')
  })

  // An open hint is not a mode: focus is still on the trigger, so a single-key
  // shortcut has to fire while one is showing.
  it('leaves the shortcuts alone while a tooltip is open', async () => {
    render(<AppShell />)
    const control = screen.getByRole('button', { name: 'Keyboard shortcuts' })
    expect(await hintOn(control)).toBe('Keyboard shortcuts')

    press('n', control)

    expect(useStore.getState().paletteDialog).toBe('launch')
  })

  // A tooltip closes on the first key of any kind. Where that key is Escape,
  // React Aria stops it rather than marking it handled, so the shell never
  // hears that one and the run stays open - one press to dismiss the tooltip,
  // and the next leaves, which is the whole cost of showing hints on focus.
  it('lets a tooltip take the first Escape and no more than that', async () => {
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1' } } })
    render(<AppShell />)
    const control = screen.getByRole('button', { name: 'Keyboard shortcuts' })
    await hintOn(control)

    await userEvent.keyboard('{Escape}')

    expect(screen.queryByRole('tooltip')).toBeNull()
    expect(useStore.getState().route.name).toBe('run')

    await userEvent.keyboard('{Escape}')

    expect(useStore.getState().route.name).toBe('board')
  })

  it.each([
    ['field', '<input />'],
    ['terminal', '<div class="xterm"><span></span></div>'],
    ['dialog', '<div role="dialog"><button type="button">ok</button></div>'],
    ['menu', '<div role="menu"><div role="menuitem">Kill run</div></div>'],
    ['list box', '<div role="listbox"><div role="option">one</div></div>'],
    ['confirm', '<div role="alertdialog"><button type="button">ok</button></div>'],
  ])('stands down while a %s has the keyboard', (_, markup) => {
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1' } } })
    render(<AppShell />)

    press('n')
    expect(useStore.getState().paletteDialog).toBe('launch')
    act(() => useStore.setState({ paletteDialog: null }))

    const host = overlay(markup)
    const target = (host.querySelector('*') ?? host) as HTMLElement
    press('n', target)
    press('Escape', target)
    press('g', target)
    press('l', target)

    expect(useStore.getState().paletteDialog).toBe(null)
    expect(useStore.getState().route.name).toBe('run')
  })

  it('stands down while a select has the keyboard', () => {
    render(<AppShell />)
    press('n')
    expect(useStore.getState().paletteDialog).toBe('launch')
    act(() => useStore.setState({ paletteDialog: null }))

    press('n', screen.getByLabelText('Workspace'))

    expect(useStore.getState().paletteDialog).toBe(null)
  })

  // Chromium drops focus to body without a focusout when the focused element
  // is removed, which is what a dialog swapping its own controls does. There
  // is no target left to ask, so the document is asked instead.
  it('stands down when focus has fallen to the body under an open dialog', () => {
    render(<AppShell />)
    press('n')
    expect(useStore.getState().paletteDialog).toBe('launch')

    overlay('<div role="dialog"><button type="button">ok</button></div>')
    act(() => useStore.setState({ paletteDialog: null }))
    press('n', document.body)

    expect(useStore.getState().paletteDialog).toBe(null)
  })

  it('leaves a modified key to the browser', () => {
    render(<AppShell />)

    press('n', window, { metaKey: true })
    press('N', window, { shiftKey: true })
    expect(useStore.getState().paletteDialog).toBe(null)

    press('n')
    expect(useStore.getState().paletteDialog).toBe('launch')
  })
})
