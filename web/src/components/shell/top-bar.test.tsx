// jsdom has no layout, so the header's widths are stubbed with what Chromium
// measures for the real fonts: each destination's width at the default and
// Larger text sizes, the 4px gap between them, and the row as the viewport
// less the brand and padding.

import { render, screen, within } from '@testing-library/react'
import { TopBar } from '@/components/shell/top-bar'
import { useStore } from '@/store'
import { serverInfo } from '@/test/fixtures'

const destinations = {
  default: [74, 84, 84, 88, 65, 113, 80, 100],
  larger: [79, 93, 94, 96, 70, 128, 88, 110],
}
const more = 28

function layOut(viewport: number, size: keyof typeof destinations) {
  const style = window.getComputedStyle
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element, pseudo) =>
    element.matches('nav[aria-label="Main navigation"] ul') ? ({ columnGap: '4px' } as CSSStyleDeclaration) : style(element, pseudo))
  const real = Element.prototype.getBoundingClientRect
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    const row = this.closest('nav[aria-label="Main navigation"] ul')
    if (this === row) {
      const tile = document.querySelector('img[src="/cloudfield-favicon.svg"]')
      return { width: viewport - (tile ? 56 : 186) } as DOMRect
    }
    if (this.parentElement === row && row) {
      const index = Array.from(row.children).indexOf(this)
      return { width: destinations[size][index] ?? more } as DOMRect
    }
    return real.call(this)
  })
}

beforeEach(() => {
  useStore.setState({
    info: serverInfo,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] },
  })
})

afterEach(() => vi.restoreAllMocks())

describe('header brand', () => {
  it.each([
    [1280, 'default', 'logo', true],
    [904, 'default', 'logo', true],
    [900, 'default', 'tile', true],
    [864, 'default', 'tile', true],
    [832, 'default', 'tile', true],
    [800, 'default', 'tile', false],
    [1280, 'larger', 'logo', true],
    [904, 'larger', 'tile', true],
    [900, 'larger', 'tile', true],
    [864, 'larger', 'tile', false],
  ] as const)('at %ipx and %s text shows the %s, Templates visible: %s', (viewport, size, brand, templates) => {
    layOut(viewport, size)
    render(<TopBar />)
    const banner = screen.getByRole('banner')
    const logo = within(banner).getAllByRole('img', { name: 'Cloudfield' })[0]!
    expect(logo.getAttribute('src')).toBe(brand === 'tile' ? '/cloudfield-favicon.svg' : '/cloudfield-logo-small.svg')
    const navigation = within(banner).getByRole('navigation', { name: 'Main navigation' })
    expect(within(navigation).queryByRole('button', { name: 'Templates' }) !== null).toBe(templates)
    expect(within(navigation).queryByRole('button', { name: 'More navigation' }) === null).toBe(templates)
  })
})
