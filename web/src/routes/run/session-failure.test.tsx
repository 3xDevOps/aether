import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import { SessionFailureCallout } from '@/routes/run/session-failure'
import type { RunShells } from '@/routes/run/shells'
import { useStore } from '@/store'
import type { AcpSession } from '@/store/sessions'
import { run, runRecords } from '@/test/fixtures'

afterEach(() => {
  vi.restoreAllMocks()
})

function signedOutSession(agentCommand?: string): AcpSession {
  return {
    turns: [],
    live: true,
    control: { has_control: true, control_session_id: 'session-1', control_generation: 2 },
    state: {
      auth: { authStatus: { kind: 'none' } },
      auth_methods: [{ id: 'login', name: 'Log in', type: 'terminal', args: ['--cli'] }],
      ...(agentCommand ? { agent_command: agentCommand } : {}),
    },
  } as unknown as AcpSession
}

function renderCallout(session: AcpSession) {
  const record = runRecords(run({ id: 'run_1', harness: 'claude', status: 'running' })).run_1!
  useStore.setState({ acpSessions: { run_1: session } })
  const shells = {
    canAdd: true,
    open: vi.fn(async () => ({ terminal_id: 'tab-1', incarnation: 'shell-1' })),
  } as unknown as RunShells
  const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
  render(<SessionFailureCallout run={record} session={session} switchable={false} shells={shells} nav={nav} />)
}

describe('terminal login', () => {
  it('types the ACP server command with the method args, not the agent name', async () => {
    const input = vi.spyOn(api, 'devTerminalInput').mockResolvedValue({ accepted: true })
    renderCallout(signedOutSession('claude-agent-acp'))
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await waitFor(() => expect(input).toHaveBeenCalledWith(expect.objectContaining({ text: 'claude-agent-acp --cli' })))
  })

  it('types nothing when the server names no ACP command', async () => {
    const input = vi.spyOn(api, 'devTerminalInput').mockResolvedValue({ accepted: true })
    renderCallout(signedOutSession())
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Log in' })).toBeDefined())
    expect(input).not.toHaveBeenCalled()
  })
})

describe('open in Standard', () => {
  it('relaunches on the account, fallback and repositories of the run it replaces', async () => {
    const launch = vi.spyOn(api, 'runLaunch').mockResolvedValue(run({ id: 'run_2', status: 'running' }))
    vi.spyOn(api, 'runClose').mockResolvedValue(run({ id: 'run_1', status: 'completed' }))
    const record = runRecords(run({
      id: 'run_1', harness: 'claude', status: 'running', member_id: 'mem-me', account_member_id: 'mem-me', requested_account_member_id: 'mem-ada',
      model: 'opus', fallback_account_member_ids: ['mem-me'],
      repos: [{ workspace_id: 'ws-1', name: 'api', path: '/repos/api', branch: 'b', primary: true }, { workspace_id: 'ws-2', name: 'web', path: '/repos/web', branch: 'b', primary: false }],
    })).run_1!
    const session = { turns: [{ items: [{ kind: 'notice', notice: { title: 'Enhanced session failed' } }] }], live: false } as unknown as AcpSession
    useStore.setState({ acpSessions: { run_1: session }, info: { member: { id: 'mem-me' } } as never })
    const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
    render(<SessionFailureCallout run={record} session={session} switchable={false} shells={{} as RunShells} nav={nav} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open in Standard' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Start a Standard run' }))
    await waitFor(() => expect(launch).toHaveBeenCalledWith({
      workspace_id: record.workspace_id, task: record.task, harness: 'claude', mode: 'tui', model: 'opus',
      account_member_id: 'mem-ada', fallback_account_member_ids: ['mem-me'], extra_workspace_ids: ['ws-2'],
    }))
  })

  it("launches a teammate's run on the viewer's own account", async () => {
    const launch = vi.spyOn(api, 'runLaunch').mockResolvedValue(run({ id: 'run_2', status: 'running' }))
    vi.spyOn(api, 'runClose').mockResolvedValue(run({ id: 'run_1', status: 'completed' }))
    const record = runRecords(run({ id: 'run_1', harness: 'claude', status: 'running', member_id: 'mem-ada', account_member_id: 'mem-carol', no_fallback: true })).run_1!
    const session = { turns: [{ items: [{ kind: 'notice', notice: { title: 'Enhanced session failed' } }] }], live: false } as unknown as AcpSession
    useStore.setState({ acpSessions: { run_1: session }, info: { member: { id: 'mem-me' } } as never })
    const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
    render(<SessionFailureCallout run={record} session={session} switchable={false} shells={{} as RunShells} nav={nav} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open in Standard' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Start a Standard run' }))
    await waitFor(() => expect(launch).toHaveBeenCalledWith({ workspace_id: record.workspace_id, task: record.task, harness: 'claude', mode: 'tui' }))
  })
})
