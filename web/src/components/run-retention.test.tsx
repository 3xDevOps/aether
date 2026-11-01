import { render, screen } from '@testing-library/react'
import { RunRetention } from '@/components/run-retention'
import { run } from '@/test/fixtures'

const reopen = /Relaunch is available until then; closing the relaunched session starts a new grace period\./
const until = '2026-10-18T23:34:23Z'

describe('run retention', () => {
  it('mentions Relaunch only for a run that can be relaunched', () => {
    const closed = render(
      <RunRetention run={run({ status: 'merged', reason: 'closed; retained container', container_retained_until: until })} />,
    )
    expect(screen.getByText(reopen)).toBeDefined()
    closed.unmount()

    render(
      <RunRetention run={run({ status: 'completed', mode: 'headless', reason: 'agent reported success; retained container', container_retained_until: until })} />,
    )
    expect(screen.getByText(/Runtime grace ends/)).toBeDefined()
    expect(screen.queryByText(reopen)).toBeNull()
  })
})
