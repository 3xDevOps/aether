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
  it('names its own ceiling and disables the add control at it', () => {
    render(
      createElement(Dock, {
        tabs: [{ id: 't1', label: 't1' }, { id: 't2', label: 't2' }],
        activeTab: 't1',
        onSelectTab: vi.fn(),
        onAddTab: vi.fn(),
        maxTabs: 2,
        height: 240,
        onHeightChange: vi.fn(),
        collapsed: false,
        onToggleCollapse: vi.fn(),
        children: createElement('div'),
      }),
    )
    const add = screen.getByRole('button', { name: 'Add terminal tab' }) as HTMLButtonElement
    expect(add.disabled).toBe(true)
    // The sentence is what a member reads, and it arrives without warning, so
    // it is announced rather than only drawn.
    expect(screen.getByRole('status')).toHaveProperty('textContent', 'At most 2 tabs')
  })
})
