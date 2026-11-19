import { render, screen } from '@testing-library/react'
import { RunRetention } from '@/components/run-retention'
import { run } from '@/test/fixtures'

const reopen = /Relaunch is available until .*Then the sandbox is deleted, but the branch and chat history stay\. Relaunching and closing it again sets a new date\./
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

  it('says a stopped sandbox keeps its disks and a paused one holds memory', () => {
    const closed = { status: 'merged', reason: 'closed; retained container', container_retained_until: until } as const
    const stopped = render(<RunRetention run={run({ ...closed, asleep_since: until })} />)
    expect(screen.getByText(/The stopped sandbox keeps only its disks; running processes do not come back\./)).toBeDefined()
    stopped.unmount()

    render(<RunRetention run={run(closed)} />)
    expect(screen.getByText(/The paused sandbox keeps holding memory meanwhile\./)).toBeDefined()
  })
})
