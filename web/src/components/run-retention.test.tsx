import { render, screen } from '@testing-library/react'
import { RunRetention } from '@/components/run-retention'
import { run } from '@/test/fixtures'

const reopen = /Exact-process Reopen ends with runtime expiry\./
const until = '2026-10-18T23:34:23Z'

describe('run retention', () => {
  it('mentions Reopen only for a run that can be reopened', () => {
    const closed = render(
      <RunRetention run={run({ status: 'merged', reason: 'closed; retained container', container_retained_until: until })} />,
    )
    expect(screen.getByText(reopen)).toBeDefined()
    closed.unmount()

    render(
      <RunRetention run={run({ status: 'completed', reason: 'worker finished; retained container', container_retained_until: until })} />,
    )
    expect(screen.getByText(/Runtime grace ends/)).toBeDefined()
    expect(screen.queryByText(reopen)).toBeNull()
  })
})
