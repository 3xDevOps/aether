import { createElement, useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Dock, clampDockHeight } from '@/components/dock'
import { atViewport } from '@/test/viewport'

describe('clampDockHeight', () => {
  it('keeps dock heights between 120px and the viewport limit', () => {
    atViewport(1440, { height: 900 })

    expect(clampDockHeight(0)).toBe(120)
    expect(clampDockHeight(500)).toBe(500)
    expect(clampDockHeight(900)).toBe(700)
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

  it('trades the drag separator for half and full heights on a touch screen', () => {
    // A phone: the separator is a drag, and a finger has no drag to give it.
    atViewport(390, { height: 844, pointer: 'coarse' })
    const onHeightChange = vi.fn()
    const props = (height: number) => ({
      tabs: [{ id: 't1', label: 't1' }],
      activeTab: 't1',
      onSelectTab: vi.fn(),
      maxTabs: 1,
      height,
      onHeightChange,
      collapsed: false,
      onToggleCollapse: vi.fn(),
      children: createElement('div'),
    })

    const view = render(createElement(Dock, props(240)))
    expect(screen.queryByRole('separator')).toBeNull()

    // Half and full are measured from the room the dock has, which here is
    // the viewport less the shell's own chrome.
    fireEvent.click(screen.getByRole('button', { name: /full screen/ }))
    expect(onHeightChange).toHaveBeenLastCalledWith(644)

    view.rerender(createElement(Dock, props(644)))
    fireEvent.click(screen.getByRole('button', { name: /half the screen/ }))
    expect(onHeightChange).toHaveBeenLastCalledWith(322)
  })

  it('focuses the expand control when Enter collapses the dock separator', () => {
    function FocusableDock() {
      const [collapsed, setCollapsed] = useState(false)
      return createElement(Dock, {
        tabs: [{ id: 't1', label: 't1' }],
        activeTab: 't1',
        onSelectTab: vi.fn(),
        maxTabs: 1,
        height: 240,
        onHeightChange: vi.fn(),
        collapsed,
        onToggleCollapse: () => setCollapsed(true),
        children: createElement('div'),
      })
    }

    render(createElement(FocusableDock))
    fireEvent.keyDown(screen.getByRole('separator', { name: 'Resize terminal dock' }), {
      key: 'Enter',
    })

    expect(screen.queryByRole('tabpanel')).toBeNull()
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'Expand terminal dock' }),
    )
  })
})
