import { act, renderHook } from '@testing-library/react'
import { useNumberTween } from '@/lib/use-number-tween'

const reduced = vi.hoisted(() => ({ value: false }))
vi.mock('@/lib/hooks', () => ({ useReducedMotion: () => reduced.value }))
vi.mock('@/lib/motion', () => ({ time: () => 500, easing: () => 'linear' }))

let frames: Map<number, FrameRequestCallback>
let nextFrame: number

function runFrame(now: number) {
  const pending = [...frames.values()]
  frames.clear()
  act(() => pending.forEach((callback) => callback(now)))
}

beforeEach(() => {
  reduced.value = false
  frames = new Map()
  nextFrame = 0
  vi.spyOn(performance, 'now').mockReturnValue(0)
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    frames.set(++nextFrame, callback)
    return nextFrame
  })
  vi.stubGlobal('cancelAnimationFrame', (id: number) => frames.delete(id))
  vi.stubGlobal('KeyframeEffect', class {})
  vi.stubGlobal(
    'Animation',
    class {
      currentTime = 0
      effect = { getComputedTiming: () => ({ progress: this.currentTime / 500 }) }
      cancel() {}
    },
  )
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('useNumberTween', () => {
  it('eases to a new target over the duration', () => {
    const { result, rerender } = renderHook(({ target }) => useNumberTween(target), { initialProps: { target: 0 } })
    rerender({ target: 100 })
    expect(result.current).toBe(0)
    runFrame(250)
    expect(result.current).toBe(50)
    runFrame(500)
    expect(result.current).toBe(100)
    expect(frames.size).toBe(0)
  })

  it('restarts from what is shown when the target changes mid-tween', () => {
    const { result, rerender } = renderHook(({ target }) => useNumberTween(target), { initialProps: { target: 0 } })
    rerender({ target: 100 })
    runFrame(250)
    rerender({ target: 0 })
    runFrame(250)
    expect(result.current).toBe(25)
  })

  it('cancels its pending frame on unmount', () => {
    const { rerender, unmount } = renderHook(({ target }) => useNumberTween(target), { initialProps: { target: 0 } })
    rerender({ target: 100 })
    expect(frames.size).toBe(1)
    unmount()
    expect(frames.size).toBe(0)
  })

  it('shows the target at once under reduced motion', () => {
    reduced.value = true
    const { result, rerender } = renderHook(({ target }) => useNumberTween(target), { initialProps: { target: 0 } })
    rerender({ target: 100 })
    expect(result.current).toBe(100)
    expect(frames.size).toBe(0)
  })
})
