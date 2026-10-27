import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import type { RoomPostResult } from '@/lib/types'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { StallCallout } from '@/routes/run/stall-recovery'
import { useStore } from '@/store'
import type { SessionRow } from '@/store/session-rows'
import { run, runRecords } from '@/test/fixtures'

afterEach(() => {
  vi.restoreAllMocks()
})

const reason = 'stalled: possibly — no progress for 6m0s, awaiting first event, transport open'
const at = '2026-08-14T10:00:00Z'

function renderCallout(rows: SessionRow[]) {
  const record = runRecords(run({ id: 'run_1', status: 'needs-attention', state: 'waiting', reason, cause: 'stall', acp: true })).run_1!
  useStore.setState({ acpSessions: { run_1: { control: { has_control: true, control_session_id: 'session-1', control_generation: 2 } } } } as never)
  const agent = { localControl: true, steerable: true } as unknown as AgentTerminal
  const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
  render(<StallCallout run={record} agent={agent} rows={rows} nav={nav} />)
}

describe('resend last prompt', () => {
  it("resends the member's last delivered prompt with its files, never Aether's reminder or a queued message", async () => {
    vi.spyOn(api, 'runGet').mockResolvedValue({ ...run({ id: 'run_1' }), liveness: { phase: 'awaiting-first-event', transport: 'open' } })
    const inject = vi.spyOn(api, 'runInject').mockResolvedValue({ message: { state: 'sent' } } as unknown as RoomPostResult)
    vi.spyOn(useStore.getState(), 'upsertRoomMessage').mockImplementation(() => {})
    renderCallout([
      { kind: 'user', id: 'a', at, body: 'fix the login bug', files: ['/workspace/.aether/terminal-files/0123456789abcdef0123456789abcdef/log.txt'], delivery: 'Sent' },
      { kind: 'user', id: 'b', at, body: 'Report your state with aether-internal report (answered, waiting) before ending the turn.' },
      { kind: 'user', id: 'c', at, body: 'and add a test', delivery: 'Queued' },
    ])
    expect(await screen.findByText('awaiting-first-event')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Resend last prompt…' }))
    expect(screen.getByText('fix the login bug')).toBeDefined()
    expect(screen.getByText('With 1 attached file.')).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Resend and spend tokens' }))
    await waitFor(() => expect(inject).toHaveBeenCalledWith('run_1', 'fix the login bug', expect.any(String), expect.objectContaining({
      attachments: ['/workspace/.aether/terminal-files/0123456789abcdef0123456789abcdef/log.txt'],
    })))
  })

  it('retries a resend whose reply was lost under the same key, so the server queues it once', async () => {
    vi.spyOn(api, 'runGet').mockResolvedValue(run({ id: 'run_1' }))
    const inject = vi.spyOn(api, 'runInject')
      .mockRejectedValueOnce(new Error('connection reset'))
      .mockResolvedValue({ message: { state: 'sent' } } as unknown as RoomPostResult)
    vi.spyOn(useStore.getState(), 'upsertRoomMessage').mockImplementation(() => {})
    renderCallout([{ kind: 'user', id: 'a', at, body: 'fix the login bug', delivery: 'Sent' }])
    for (let i = 1; i <= 2; i++) {
      fireEvent.click(await screen.findByRole('button', { name: 'Resend last prompt…' }))
      fireEvent.click(screen.getByRole('button', { name: 'Resend and spend tokens' }))
      await waitFor(() => expect(inject).toHaveBeenCalledTimes(i))
      await waitFor(() => expect(screen.getByRole('button', { name: 'Resend last prompt…' }).hasAttribute('disabled')).toBe(false))
    }
    expect(inject.mock.calls[1]![2]).toBe(inject.mock.calls[0]![2])
  })

  it('offers no resend for a prompt with images, which the composer must send', async () => {
    vi.spyOn(api, 'runGet').mockResolvedValue(run({ id: 'run_1' }))
    renderCallout([{ kind: 'user', id: 'a', at, body: 'match this layout', images: [{ messageID: 'm1', index: 0 }], delivery: 'Sent' }])
    expect((await screen.findByRole('button', { name: 'Resend last prompt…' })).hasAttribute('disabled')).toBe(true)
  })
})
