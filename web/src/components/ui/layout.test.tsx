import { act, fireEvent, render, renderHook, screen } from '@testing-library/react'
import { Bot } from '@/components/icons'
import { EmptyState } from '@/components/ui/empty-state'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { PageBody } from '@/components/ui/page-body'
import { StatTile } from '@/components/ui/stat-tile'
import { useTween } from '@/lib/use-tween'

describe('PageBody', () => {
  it('centres one column at the named width and fades in', () => {
    render(<PageBody width="xl" data-testid="body" />)
    const cls = screen.getByTestId('body').getAttribute('class')!
    expect(cls).toContain('max-w-6xl')
    expect(cls).toContain('gap-8')
    expect(cls).toContain('animate-fade-in')
    expect(cls).toContain('motion-reduce:animate-none')
  })

  it('lets a page tighten its own gap', () => {
    render(<PageBody className="gap-6" data-testid="body" />)
    const cls = screen.getByTestId('body').getAttribute('class')!
    expect(cls).toContain('max-w-3xl')
    expect(cls).toContain('gap-6')
    expect(cls).not.toContain('gap-8')
  })
})

describe('InsetList', () => {
  it('is a plain row without onOpen', () => {
    render(<InsetList aria-label="Things"><InsetListItem>One</InsetListItem></InsetList>)
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.getByRole('list', { name: 'Things' }).getAttribute('class')).toContain('divide-y')
  })

  it('makes the whole row one button named by its content', () => {
    const open = vi.fn()
    render(
      <InsetList>
        <InsetListItem onOpen={open} className="flex-col">
          <span>session run_1</span>
          <span>4 KB</span>
        </InsetListItem>
      </InsetList>,
    )
    const button = screen.getByRole('button', { name: /^session run_1/ })
    expect(button.getAttribute('class')).toContain('flex-col')
    expect(button.closest('li')!.getAttribute('class')).toContain('hover:bg-hover')
    fireEvent.click(button)
    expect(open).toHaveBeenCalledTimes(1)
  })
})

describe('StatTile', () => {
  it('names itself by its label and puts the chart at the foot', () => {
    render(<StatTile label="Success rate" value="40%" spark={<i data-testid="spark" />}>Runs: 2 of 5</StatTile>)
    const tile = screen.getByRole('article', { name: 'Success rate' })
    expect(tile.textContent).toContain('40%')
    expect(tile.querySelector('p')!.getAttribute('class')).toContain('text-stat')
    expect(screen.getByTestId('spark').parentElement!.getAttribute('class')).toContain('mt-auto')
  })

  it('reads as a group, and compact for a line of words', () => {
    render(<StatTile role="group" label="Pressure" value="CPU 3%" compact />)
    expect(screen.getByRole('group', { name: 'Pressure' }).querySelector('p')!.getAttribute('class')).toContain('text-prose')
  })
})

describe('EmptyState', () => {
  it('draws its icon in a framed tile above the title', () => {
    render(<EmptyState icon={<Bot />} title="No agents" action={<button type="button">Add</button>}>Nothing here.</EmptyState>)
    const tile = document.querySelector('[data-slot="icon-tile"]')!
    expect(tile.querySelector('svg')).not.toBeNull()
    expect(tile.nextElementSibling!.textContent).toBe('No agents')
  })

  it('draws no tile without an icon', () => {
    render(<EmptyState title="Nothing" />)
    expect(document.querySelector('[data-slot="icon-tile"]')).toBeNull()
  })
})

describe('useTween', () => {
  const frames: FrameRequestCallback[] = []

  beforeEach(() => {
    document.documentElement.style.setProperty('--duration-glide', '300ms')
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => frames.push(callback))
    vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
    vi.spyOn(performance, 'now').mockReturnValue(0)
  })

  afterEach(() => {
    frames.length = 0
    document.documentElement.style.removeProperty('--duration-glide')
    vi.restoreAllMocks()
  })

  it('shows the first target at once, then glides to the next and lands on it', () => {
    const { result, rerender } = renderHook(({ value }) => useTween(value), { initialProps: { value: 10 as number | undefined } })
    expect(result.current).toBe(10)
    expect(frames).toHaveLength(0)

    rerender({ value: 20 })
    expect(frames).toHaveLength(1)
    act(() => frames.shift()!(150))
    expect(result.current).toBeGreaterThan(10)
    expect(result.current).toBeLessThan(20)
    act(() => frames.shift()!(300))
    expect(result.current).toBe(20)
    expect(frames).toHaveLength(0)
  })

  it('shows the first value at once when it arrives after mount', () => {
    const { result, rerender } = renderHook(({ value }) => useTween(value), { initialProps: { value: undefined as number | undefined } })
    expect(result.current).toBeUndefined()
    rerender({ value: 42 })
    expect(result.current).toBe(42)
    expect(frames).toHaveLength(0)
  })

  it('treats a reading that is not finite as none and recovers on the next valid one', () => {
    const { result, rerender } = renderHook(({ value }) => useTween(value), { initialProps: { value: 10 as number | undefined } })
    rerender({ value: Infinity })
    expect(result.current).toBeUndefined()
    rerender({ value: 30 })
    expect(result.current).toBe(30)
    expect(frames).toHaveLength(0)
  })

  it('snaps under reduced motion', () => {
    vi.spyOn(window, 'matchMedia').mockImplementation(
      (query: string) => ({ matches: query.includes('prefers-reduced-motion'), addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList,
    )
    const { result, rerender } = renderHook(({ value }) => useTween(value), { initialProps: { value: 10 } })
    rerender({ value: 20 })
    expect(result.current).toBe(20)
    expect(frames).toHaveLength(0)
  })
})
