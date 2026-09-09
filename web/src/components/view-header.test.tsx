import { render, screen } from '@testing-library/react'
import { ViewHeader } from '@/components/view-header'

// The classes the run header's own split reads: `@container/header` is what
// makes every `@4xl/header:` utility on the action bar mean this row's width,
// and without it they all go dead at once. jsdom evaluates no container
// query, so this proves the wiring and nothing about the resulting layout.
test('the row is the named container its actions size against', () => {
  render(<ViewHeader title="Run 1" subtitle="ship the thing" actions={<button>Kill</button>} />)

  expect(screen.getByRole('banner').className).toMatch(/@container\/header/)

  // The row cannot scroll sideways, so the actions must not be squeezed past
  // the right edge by a long title.
  expect(screen.getByRole('toolbar', { name: 'Run 1 actions' }).className).toMatch(
    /\bshrink-0\b/,
  )
})

// routes/run.tsx passes the whole task as the subtitle, so a deficit split in
// proportion to natural width would eat the run's own name first.
test('the subtitle gives way before the run title does', () => {
  render(<ViewHeader title="Run 1" subtitle="ship the thing" actions={<button>Kill</button>} />)

  const heading = screen.getByRole('heading', { name: 'Run 1' })
  expect(heading.className).toMatch(/\bmin-w-0\b/)
  expect(heading.className).toMatch(/\bmax-w-64\b/)

  const subtitle = screen.getByTitle('ship the thing')
  expect(subtitle.className).toMatch(/\bmin-w-0\b/)
  expect(subtitle.className).toMatch(/\bflex-1\b/)

  // The pair share one shrinkable slot, so neither can push the actions off.
  const pair = heading.parentElement?.className ?? ''
  expect(pair).toMatch(/\bmin-w-0\b/)
  expect(pair).toMatch(/\bflex-1\b/)
})
