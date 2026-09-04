import { createElement } from 'react'
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Dock, clampDockHeight } from '@/components/dock'

describe('clampDockHeight', () => {
  it('keeps dock heights between 120px and the viewport limit', () => {
    const originalHeight = window.innerHeight
    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      value: 900,
    })

    expect(clampDockHeight(0)).toBe(120)
    expect(clampDockHeight(500)).toBe(500)
    expect(clampDockHeight(900)).toBe(700)

    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      value: originalHeight,
    })
  })
})
describe('Dock controls', () => {
  it('disables the add control when the caller reaches its cap', () => {
    render(
      createElement(Dock, {
        tabs: [],
        activeTab: '',
        onSelectTab: vi.fn(),
        onAddTab: vi.fn(),
        addDisabled: true,
        height: 240,
        onHeightChange: vi.fn(),
        collapsed: false,
        onToggleCollapse: vi.fn(),
        children: createElement('div'),
      }),
    )
    expect((screen.getByRole('button', { name: 'Add terminal tab' }) as HTMLButtonElement).disabled).toBe(
      true,
    )
  })
})
