import { act, renderHook } from '@testing-library/react'
import { afterEach, expect, test } from 'vitest'
import { useKeyboardInset } from '@/lib/keyboard-inset'

class FakeViewport extends EventTarget {
  height = window.innerHeight
  offsetTop = 0
}

afterEach(() => {
  Reflect.deleteProperty(window, 'visualViewport')
})

test('a keyboard that only shrinks the visual viewport lifts sheets by its height', () => {
  const viewport = new FakeViewport()
  Object.defineProperty(window, 'visualViewport', { value: viewport, configurable: true })
  const root = document.documentElement
  const { unmount } = renderHook(() => useKeyboardInset())
  expect(root.style.getPropertyValue('--keyboard-inset')).toBe('0px')

  act(() => {
    viewport.height = window.innerHeight - 300
    viewport.dispatchEvent(new Event('resize'))
  })
  expect(root.style.getPropertyValue('--keyboard-inset')).toBe('300px')

  act(() => {
    viewport.offsetTop = 100
    viewport.dispatchEvent(new Event('scroll'))
  })
  expect(root.style.getPropertyValue('--keyboard-inset')).toBe('200px')

  unmount()
  expect(root.style.getPropertyValue('--keyboard-inset')).toBe('')
})
