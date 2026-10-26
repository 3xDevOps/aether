import { act, fireEvent, render, screen } from '@testing-library/react'
import { ProgressBar } from '@/components/ui/progress-bar'
import { SlidingIndicator } from '@/components/ui/sliding-indicator'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { TextSwap } from '@/components/ui/text-swap'

function rect(left: number, width: number): DOMRect {
  return { left, top: 0, width, height: 28, right: left + width, bottom: 28, x: left, y: 0, toJSON: () => ({}) }
}

// jsdom lays nothing out, so each tab reports a box of its own.
function layOutTabs() {
  return vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    if (this.dataset.indicatorKey === 'session') return rect(10, 60)
    if (this.dataset.indicatorKey === 'changes') return rect(80, 70)
    if (this.dataset.slot === 'sliding-indicator') return rect(parseFloat(this.style.left), parseFloat(this.style.width))
    return rect(0, 300)
  })
}

function reduceMotion() {
  return vi.spyOn(window, 'matchMedia').mockImplementation(
    (query: string) => ({ matches: query.includes('prefers-reduced-motion'), addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList,
  )
}

function RunViews() {
  return (
    <Tabs defaultValue="session">
      <TabsList aria-label="Run views">
        <TabsTrigger value="session">Session</TabsTrigger>
        <TabsTrigger value="changes">Changes</TabsTrigger>
      </TabsList>
    </Tabs>
  )
}

function choose(name: string) {
  fireEvent.mouseDown(screen.getByRole('tab', { name }), { button: 0, ctrlKey: false })
}

describe('tab indicator', () => {
  afterEach(() => vi.restoreAllMocks())

  it('sits on the active tab, then glides to the next one', () => {
    layOutTabs()
    const animate = vi.spyOn(Element.prototype, 'animate')
    render(<RunViews />)
    const indicator = document.querySelector<HTMLElement>('[data-slot="sliding-indicator"]')!
    expect(indicator.getAttribute('aria-hidden')).toBe('true')
    expect([indicator.style.left, indicator.style.width, indicator.style.opacity]).toEqual(['10px', '60px', '1'])
    expect(animate).not.toHaveBeenCalled()

    choose('Changes')

    expect(screen.getByRole('tab', { name: 'Changes' }).getAttribute('data-state')).toBe('active')
    expect([indicator.style.left, indicator.style.width]).toEqual(['80px', '70px'])
    expect(animate).toHaveBeenCalledTimes(1)
    const [frames] = animate.mock.calls[0] as [Keyframe[]]
    expect(frames.map((f) => [f.left, f.width])).toEqual([
      ['10px', '60px'],
      ['80px', '70px'],
    ])
  })

  it('measures in layout pixels while an ancestor is scaled', () => {
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(function (this: HTMLElement) {
      return this.dataset.slot === 'tabs-list' ? 300 : 0
    })
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.dataset.indicatorKey === 'session') return rect(9.5, 57)
      if (this.dataset.indicatorKey === 'changes') return rect(76, 66.5)
      return rect(0, 285)
    })
    render(<RunViews />)
    const indicator = document.querySelector<HTMLElement>('[data-slot="sliding-indicator"]')!
    expect([indicator.style.left, indicator.style.width]).toEqual(['10px', '60px'])
  })

  it('jumps under reduced motion', () => {
    layOutTabs()
    reduceMotion()
    const animate = vi.spyOn(Element.prototype, 'animate')
    render(<RunViews />)
    choose('Changes')
    expect(document.querySelector<HTMLElement>('[data-slot="sliding-indicator"]')!.style.left).toBe('80px')
    expect(animate).not.toHaveBeenCalled()
  })

  function stubGlide() {
    let finish = () => {}
    const cancel = vi.fn()
    const finished = new Promise<void>((resolve) => (finish = resolve))
    vi.spyOn(Element.prototype, 'animate').mockReturnValue({ finished, cancel } as unknown as Animation)
    return { cancel, finish: () => act(async () => finish()) }
  }

  it('measures again when a tab resized during the glide', async () => {
    let changesWidth = 70
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.dataset.indicatorKey === 'session') return rect(10, 60)
      if (this.dataset.indicatorKey === 'changes') return rect(80, changesWidth)
      if (this.dataset.slot === 'sliding-indicator') return rect(parseFloat(this.style.left), parseFloat(this.style.width))
      return rect(0, 300)
    })
    const glide = stubGlide()
    render(<RunViews />)
    choose('Changes')
    changesWidth = 90
    await glide.finish()
    expect(document.querySelector<HTMLElement>('[data-slot="sliding-indicator"]')!.style.width).toBe('90px')
  })

  it('watches the tabs it mounts with and stops watching tabs that left the strip', () => {
    layOutTabs()
    const observe = vi.fn()
    const unobserve = vi.fn()
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe = observe
        unobserve = unobserve
        disconnect() {}
      },
    )
    const strip = (keys: string[]) => (
      <div style={{ position: 'relative' }}>
        <SlidingIndicator activeKey="session" />
        {keys.map((key) => (
          <span key={key} data-indicator-key={key} />
        ))}
      </div>
    )
    const { rerender } = render(strip(['session', 'changes']))
    const keys = observe.mock.calls.map(([el]) => (el as HTMLElement).dataset.indicatorKey)
    expect(keys).toEqual(expect.arrayContaining(['session', 'changes']))
    rerender(strip(['session']))
    expect(unobserve).toHaveBeenCalledTimes(1)
    expect((unobserve.mock.calls[0][0] as HTMLElement).dataset.indicatorKey).toBe('changes')
    vi.unstubAllGlobals()
  })

  it('cancels a running glide when reduced motion turns on', () => {
    layOutTabs()
    let listener = () => {}
    let matches = false
    vi.spyOn(window, 'matchMedia').mockImplementation(
      () =>
        ({
          get matches() {
            return matches
          },
          addEventListener: (_: string, fn: () => void) => (listener = fn),
          removeEventListener() {},
        }) as unknown as MediaQueryList,
    )
    const glide = stubGlide()
    render(<RunViews />)
    choose('Changes')
    expect(glide.cancel).not.toHaveBeenCalled()
    matches = true
    act(() => listener())
    expect(glide.cancel).toHaveBeenCalled()
    expect(document.querySelector<HTMLElement>('[data-slot="sliding-indicator"]')!.style.left).toBe('80px')
  })
})

describe('text swap', () => {
  afterEach(() => vi.restoreAllMocks())

  it('replaces the old copy at once and fades the new one in', () => {
    const animate = vi.spyOn(Element.prototype, 'animate')
    const { container, rerender } = render(<TextSwap>Working</TextSwap>)
    expect(animate).not.toHaveBeenCalled()
    rerender(<TextSwap>Needs you</TextSwap>)
    expect(container.textContent).toBe('Needs you')
    expect(animate).toHaveBeenCalled()
  })

  it('keeps the outgoing copy from assistive technology while it fades, then drops it', async () => {
    let finish!: () => void
    const finished = new Promise<void>((resolve) => (finish = resolve))
    vi.spyOn(Element.prototype, 'animate').mockImplementation(() => ({ finished, cancel() {} }) as unknown as Animation)
    const { container, rerender } = render(<TextSwap mode="crossfade">Copy</TextSwap>)
    rerender(<TextSwap mode="crossfade">Copied</TextSwap>)
    const [outgoing, incoming] = container.querySelectorAll('[data-slot="swap"] > span')
    expect(outgoing.textContent).toBe('Copy')
    expect(outgoing.getAttribute('aria-hidden')).toBe('true')
    expect(incoming.hasAttribute('aria-hidden')).toBe(false)
    await act(async () => finish())
    expect(container.textContent).toBe('Copied')
  })

  it('swaps without motion under reduced motion, and updates in place under the same key', () => {
    reduceMotion()
    const animate = vi.spyOn(Element.prototype, 'animate')
    const { container, rerender } = render(<TextSwap swapKey="working">Working 1m</TextSwap>)
    rerender(<TextSwap swapKey="working">Working 2m</TextSwap>)
    rerender(<TextSwap swapKey="done">Done</TextSwap>)
    expect(container.textContent).toBe('Done')
    expect(animate).not.toHaveBeenCalled()
  })
})

test('a progress bar with a role reports its value; without one it only draws', () => {
  const { container } = render(
    <>
      <ProgressBar role="meter" aria-label="Disk used" value={150} max={100} />
      <ProgressBar aria-hidden value={25} />
    </>,
  )
  const meter = screen.getByRole('meter', { name: 'Disk used' })
  expect([meter.getAttribute('aria-valuemin'), meter.getAttribute('aria-valuemax'), meter.getAttribute('aria-valuenow')]).toEqual(['0', '100', '100'])
  const [full, quarter] = container.querySelectorAll<HTMLElement>('[data-slot="progress-fill"]')
  expect(full.style.width).toBe('100%')
  expect(quarter.style.width).toBe('25%')
  expect(container.querySelectorAll('[role]')).toHaveLength(1)
})

test('a progress bar with no value sweeps its track and reports no value', () => {
  render(<ProgressBar role="progressbar" aria-label="Uploading" />)
  const bar = screen.getByRole('progressbar', { name: 'Uploading' })
  expect(bar.hasAttribute('aria-valuenow')).toBe(false)
  const fill = bar.querySelector<HTMLElement>('[data-slot="progress-fill"]')!
  expect(fill.className).toContain('animate-progress-sweep')
  expect(fill.style.width).toBe('')
})
