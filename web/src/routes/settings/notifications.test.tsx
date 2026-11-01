import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError } from '@/lib/api'
import type { SlackStatusResult } from '@/lib/types'
import { disablePush, enablePush, readPush, testPush, type PushState } from '@/lib/push'
import { PushSection, SlackSection } from '@/routes/settings/notifications'
import { useStore } from '@/store'
import { fakeApi } from '@/test/fixtures'

vi.mock('@/lib/push', () => ({
  readPush: vi.fn(),
  enablePush: vi.fn(),
  disablePush: vi.fn(),
  testPush: vi.fn(),
}))

const label = 'Notify this device when a session needs me'

function show(state: PushState, slack?: SlackStatusResult) {
  vi.mocked(readPush).mockResolvedValue(state)
  useStore.setState({ capabilities: { gateway: 'server', methods: ['*'], ws: [] } })
  const client = fakeApi()
  if (slack) vi.mocked(client.slackStatus).mockResolvedValue(slack)
  render(<><PushSection client={client} /><SlackSection client={client} /></>)
  return client
}

const toggle = () => screen.getByRole('checkbox', { name: label })

describe('notification settings', () => {
  beforeEach(() => vi.resetAllMocks())

  it('reads this device through the gateway that serves the dashboard', async () => {
    const client = show({ kind: 'off' })
    expect(screen.getByText('Reading this device…')).toBeDefined()
    expect(toggle()).toHaveProperty('disabled', true)
    await waitFor(() => expect(toggle()).toHaveProperty('disabled', false))
    expect(readPush).toHaveBeenCalledWith(client, 'server')
    expect(toggle().getAttribute('aria-checked')).toBe('false')
    expect(screen.queryByRole('button', { name: 'Send test notification' })).toBeNull()
  })

  it.each([
    [{ kind: 'blocked' }, 'This browser blocks notifications from this dashboard.', undefined],
    [{ kind: 'unsupported', why: 'desktop' }, 'The desktop app notifies you itself while it is open.', '#the-dashboard'],
    [{ kind: 'unsupported', why: 'gateway' }, 'serves this dashboard from your computer', '#the-dashboard'],
    [{ kind: 'unsupported', why: 'home-screen' }, 'On iPhone and iPad, notifications reach Aether only when it is opened from the home screen.', '#add-it-to-your-home-screen'],
    [{ kind: 'unsupported', why: 'browser' }, 'This browser cannot receive push notifications.', '#add-it-to-your-home-screen'],
  ] as [PushState, string, string | undefined][])('says why it cannot be turned on: %j', async (state, reason, anchor) => {
    show(state)
    expect(await screen.findByText(reason, { exact: false })).toBeDefined()
    expect(toggle()).toHaveProperty('disabled', true)
    expect(toggle().getAttribute('aria-checked')).toBe('false')
    const link = screen.queryAllByRole('link').find((a) => a.getAttribute('href')?.includes('networking.md'))
    expect(link?.getAttribute('href')).toBe(
      anchor && `https://github.com/3xDevOps/Aether/blob/main/docs/networking.md${anchor}`,
    )
  })

  it('turns on, sends a test, and turns off', async () => {
    const user = userEvent.setup()
    const client = show({ kind: 'off' })
    vi.mocked(enablePush).mockResolvedValue({ kind: 'on' })
    vi.mocked(testPush).mockResolvedValue()
    vi.mocked(disablePush).mockResolvedValue({ kind: 'off' })
    await waitFor(() => expect(toggle()).toHaveProperty('disabled', false))

    await user.click(toggle())
    expect(enablePush).toHaveBeenCalledWith(client)
    await waitFor(() => expect(toggle().getAttribute('aria-checked')).toBe('true'))

    await user.click(screen.getByRole('button', { name: 'Send test notification' }))
    expect((await screen.findByRole('status')).textContent).toBe('Sent. It should appear on this device in a few seconds.')

    await user.click(toggle())
    expect(disablePush).toHaveBeenCalledWith(client)
    await waitFor(() => expect(toggle().getAttribute('aria-checked')).toBe('false'))
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Send test notification' })).toBeNull()
  })

  it('shows blocked when the browser refuses the prompt', async () => {
    const user = userEvent.setup()
    show({ kind: 'off' })
    vi.mocked(enablePush).mockResolvedValue({ kind: 'blocked' })
    await waitFor(() => expect(toggle()).toHaveProperty('disabled', false))

    await user.click(toggle())

    expect(await screen.findByText('This browser blocks notifications', { exact: false })).toBeDefined()
    expect(toggle()).toHaveProperty('disabled', true)
  })

  it('shows the error the browser or the server gave, and stays as it was', async () => {
    const user = userEvent.setup()
    show({ kind: 'off' })
    vi.mocked(enablePush).mockRejectedValue(new Error('Registration failed - push service error'))
    await waitFor(() => expect(toggle()).toHaveProperty('disabled', false))

    await user.click(toggle())

    expect((await screen.findByRole('alert')).textContent).toBe('Registration failed - push service error')
    expect(toggle().getAttribute('aria-checked')).toBe('false')
    expect(toggle()).toHaveProperty('disabled', false)
  })

  it('shows why a test was not delivered, and that the server dropped the device', async () => {
    const user = userEvent.setup()
    show({ kind: 'on' })
    vi.mocked(testPush).mockRejectedValueOnce(new ApiError(
      503,
      'push.test: push: the push service did not take the notification: fcm.googleapis.com: dial tcp: lookup fcm.googleapis.com: no such host',
    ))
    const test = await screen.findByRole('button', { name: 'Send test notification' })

    await user.click(test)
    expect((await screen.findByRole('alert')).textContent).toBe(
      'the push service did not take the notification: fcm.googleapis.com: dial tcp: lookup fcm.googleapis.com: no such host',
    )
    expect(toggle().getAttribute('aria-checked')).toBe('true')

    vi.mocked(readPush).mockResolvedValue({ kind: 'off' })
    vi.mocked(testPush).mockRejectedValueOnce(new ApiError(
      503,
      'push.test: push: the push service did not take the notification: the push service no longer has this subscription',
    ))
    await user.click(test)
    await waitFor(() => expect(toggle().getAttribute('aria-checked')).toBe('false'))
    expect(screen.getByRole('alert').textContent).toContain('the push service no longer has this subscription')
  })

  describe('Slack', () => {
    const link = { user_id: 'U123', team_id: 'T123', linked_at: '2026-10-25T12:00:00Z' }

    it('points to the admin command when the server has no bot', async () => {
      show({ kind: 'off' })
      expect(screen.getByText('Reading Slack…')).toBeDefined()
      expect(await screen.findByText('sudo aether-server slack setup')).toBeDefined()
      expect(screen.getByRole('link', { name: 'Slack guide' }).getAttribute('href')).toBe(
        'https://github.com/3xDevOps/Aether/blob/main/docs/slack.md',
      )
      expect(screen.queryByRole('button', { name: 'Link Slack' })).toBeNull()
    })

    it('shows the code to send the bot, then the link once it is made', async () => {
      const user = userEvent.setup()
      const client = show({ kind: 'off' }, { configured: true, connected: true, bot: 'Aether', team: 'Acme' })
      vi.mocked(client.slackLink).mockResolvedValue({ code: 'K7Q2-M9XD', expires_at: '2026-10-25T12:10:00Z', bot: 'Aether', team: 'Acme' })

      await user.click(await screen.findByRole('button', { name: 'Link Slack' }))
      expect((await screen.findByText('K7Q2-M9XD')).parentElement?.textContent).toBe(
        'Send this code to Aether in Slack (Acme) within 10 minutes: K7Q2-M9XD',
      )

      vi.mocked(client.slackLink).mockResolvedValue({ code: 'P4WN-8RTZ', expires_at: '2026-10-25T12:20:00Z', bot: 'Aether', team: 'Acme' })
      await user.click(screen.getByRole('button', { name: 'New code' }))
      expect(await screen.findByText('P4WN-8RTZ')).toBeDefined()

      vi.mocked(client.slackStatus).mockResolvedValue({ configured: true, connected: true, bot: 'Aether', team: 'Acme', link })
      await user.click(screen.getByRole('button', { name: 'Check again' }))
      expect(await screen.findByText('Linked to Slack user U123.', { exact: false })).toBeDefined()
      expect(screen.queryByText('P4WN-8RTZ')).toBeNull()
    })

    it('unlinks and shows the server error when it is not connected', async () => {
      const user = userEvent.setup()
      const client = show({ kind: 'off' }, { configured: true, connected: false, error: 'invalid_auth', link })

      expect((await screen.findByText('Linked to Slack user U123.', { exact: false })).textContent).toContain('the Aether bot')
      expect(screen.getByText('The server is not connected to Slack: invalid_auth')).toBeDefined()

      vi.mocked(client.slackStatus).mockResolvedValue({ configured: true, connected: false, error: 'invalid_auth' })
      await user.click(screen.getByRole('button', { name: 'Unlink' }))
      expect(client.slackUnlink).toHaveBeenCalled()
      expect(await screen.findByRole('button', { name: 'Link Slack' })).toBeDefined()
    })
  })
})
