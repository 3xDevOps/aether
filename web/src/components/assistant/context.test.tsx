import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AppShell } from '@/components/shell/app-shell'
import { CheckPills } from '@/components/workflow/git-checks'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { AssistantStatus, ViewContext } from '@/lib/assistant-types'
import type { RunPRFeedbackResult } from '@/lib/types'
import { trackPullRequest } from '@/routes/diff/publish-pr'
import { useStore } from '@/store'
import { duplicateSendWindow, promptKey, settlePromptKey } from '@/store/assistant'
import { item, resetItems, ScriptedSession, say, state } from '@/test/acp-stream'
import { alice, run, runRecords, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

Element.prototype.scrollIntoView = vi.fn()

class SizedObserver {
  constructor(private readonly callback: ResizeObserverCallback) {}
  observe(target: Element) {
    const box = [{ blockSize: 24, inlineSize: 400 }]
    this.callback([{ target, contentRect: { height: 24, width: 400 }, borderBoxSize: box, contentBoxSize: box } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
  }
  unobserve() {}
  disconnect() {}
}

function status(over: Partial<AssistantStatus> = {}): AssistantStatus {
  return { state: 'idle', agent: 'claude', agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }], confirmations: [], auto_approve: false, suggest_auto_approve: false, share_view: true, ...over }
}

const login = run({ id: 'run-login', title: 'Fix login test' })
const docs = run({ id: 'run-docs', title: 'Ship the docs' })

const feedback = {
  identity: 'alice',
  account_member_id: alice.id,
  truncated: false,
  pull_request: { number: 42, url: 'https://github.com/acme/api/pull/42', state: 'open', title: 'Fix login', draft: false, repository: 'acme/api', base_branch: 'main', head_repository: 'acme/api', head_branch: 'fix', head_oid: 'abc' },
  checks: [{ name: 'lint', status: 'COMPLETED', conclusion: 'FAILURE' }, { name: 'build', status: 'COMPLETED', conclusion: 'SUCCESS' }],
  comments: [],
  reviews: [],
  review_comments: [],
} as unknown as RunPRFeedbackResult

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', SizedObserver)
  StubSocket.install()
  resetItems()
  localStorage.clear()
  useStore.setState({
    ...useStore.getInitialState(),
    info: serverInfo,
    members: { [alice.id]: alice },
    workspaces: { [workspace.id]: workspace },
    runs: runRecords(login, docs),
    hydrated: true,
    route: { name: 'run', params: { runId: login.id } },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
    onboarded: true,
  }, true)
  vi.mocked(api.assistantStatus).mockResolvedValue(status())
  vi.mocked(api.assistantPrompt).mockResolvedValue({})
})

afterEach(async () => {
  cleanup()
  await Promise.resolve()
  vi.clearAllMocks()
  vi.unstubAllGlobals()
})

function assistantSocket(): ScriptedSession {
  const socket = StubSocket.opened.filter((s) => s.url.includes('/ws/assistant')).at(-1)
  if (!socket) throw new Error('no /ws/assistant socket opened')
  return new ScriptedSession(socket)
}

async function openPanel(extra?: React.ReactNode) {
  render(<><AppShell />{extra}</>)
  await userEvent.click(screen.getAllByRole('button', { name: 'Assistant' })[0]!)
  const aside = await screen.findByRole('complementary', { name: 'Assistant' })
  assistantSocket().open({}, [])
  return aside
}

const box = (aside: HTMLElement) => within(aside).getByRole('textbox', { name: 'Message the assistant' }) as HTMLTextAreaElement
const sent = (call = 0) => vi.mocked(api.assistantPrompt).mock.calls[call]![2] as ViewContext | undefined

describe('pinning the page a draft was started on', () => {
  it('asks which run after navigating, and sends only the one chosen', async () => {
    const aside = await openPanel()
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'why is this failing?')
    act(() => useStore.getState().navigate('run', { runId: docs.id }))

    const asked = await within(aside).findByRole('group', { name: 'You moved to Ship the docs. Ask about:' })
    await userEvent.type(box(aside), '{Enter}')
    expect(api.assistantPrompt).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(within(asked).getByRole('button', { name: 'Ship the docs' }))

    await userEvent.click(within(asked).getByRole('button', { name: 'Fix login test (where you started)' }))
    expect(within(aside).queryByRole('group', { name: /You moved to/ })).toBeNull()
    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()?.route).toMatchObject({ name: 'run', run_id: login.id })
  })

  it('sends the page moved to once the member picks it', async () => {
    const aside = await openPanel()
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'summarize')
    act(() => useStore.getState().navigate('run', { runId: docs.id }))
    await userEvent.click(await within(aside).findByRole('button', { name: 'Ship the docs' }))
    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()?.route).toMatchObject({ run_id: docs.id })
  })

  it('follows the page while the draft is empty', async () => {
    const aside = await openPanel()
    act(() => useStore.getState().navigate('run', { runId: docs.id }))
    const chips = await within(aside).findByRole('list', { name: 'Sent with this message' })
    expect(within(chips).getByText('Ship the docs')).toBeDefined()
    expect(within(aside).queryByRole('group', { name: /You moved to/ })).toBeNull()
  })
})

describe('attachments', () => {
  it('sends a check attached from its pill after the member removes the page and navigates', async () => {
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    expect(screen.queryByRole('button', { name: 'Fix build with the assistant' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    expect(box(aside).value).toBe('Fix this failed check')
    await waitFor(() => expect(document.activeElement).toBe(box(aside)))
    const chips = within(aside).getByRole('list', { name: 'Sent with this message' })
    expect(within(chips).getByText('lint')).toBeDefined()

    const removePage = within(chips).getByRole('button', { name: 'Remove page Fix login test from the message' })
    removePage.focus()
    await userEvent.keyboard('{Delete}')
    await waitFor(() => expect(document.activeElement).toBe(within(chips).getByRole('button', { name: 'Remove check lint from the message' })))
    act(() => useStore.getState().navigate('run', { runId: docs.id }))
    expect(within(aside).queryByRole('group', { name: /You moved to/ })).toBeNull()

    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()?.route).toBeUndefined()
    expect(sent()?.recent).toBeUndefined()
    expect(sent()?.attachments).toEqual([{ kind: 'check', workspace_id: workspace.id, run_id: login.id, pr: 42, repository: 'acme/api', check: 'lint' }])
    await waitFor(() => expect(within(aside).queryByText('lint')).toBeNull())
  })

  it('makes the server forget a removed page before the prompt that follows at once', async () => {
    const order: string[] = []
    let land!: () => void
    vi.mocked(api.assistantView).mockImplementation((context) => {
      order.push(context.route ? 'page' : 'forget')
      return context.route ? Promise.resolve({}) : new Promise((resolve) => { land = () => resolve({}) })
    })
    vi.mocked(api.assistantPrompt).mockImplementationOnce(async () => {
      order.push('prompt')
      return {}
    })
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    await userEvent.click(within(aside).getByRole('button', { name: 'Remove page Fix login test from the message' }))
    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(order).toContain('forget'))
    expect(order).not.toContain('prompt')
    act(() => land())
    await waitFor(() => expect(order.at(-1)).toBe('prompt'))
    expect(order.indexOf('forget')).toBeLessThan(order.indexOf('prompt'))
    vi.mocked(api.assistantView).mockImplementation(async () => ({}))
  })

  it('sends nothing when a chip is removed while Send waits for the page to be forgotten', async () => {
    let land!: () => void
    vi.mocked(api.assistantView).mockImplementation((context) => context.route ? Promise.resolve({}) : new Promise((resolve) => { land = () => resolve({}) }))
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    await userEvent.click(within(aside).getByRole('button', { name: 'Remove page Fix login test from the message' }))
    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(land).toBeDefined())
    await userEvent.click(within(aside).getByRole('button', { name: 'Remove check lint from the message' }))
    act(() => land())
    await waitFor(() => expect(useStore.getState().assistantSending).toBe(false))
    expect(api.assistantPrompt).not.toHaveBeenCalled()
    vi.mocked(api.assistantView).mockImplementation(async () => ({}))
  })

  it('keeps a check attached while the previous send was in flight', async () => {
    let reply!: () => void
    vi.mocked(api.assistantPrompt).mockImplementationOnce(() => new Promise((resolve) => { reply = () => resolve({}) }))
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await userEvent.type(box(aside), 'Why is this stuck?{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    act(() => reply())
    await waitFor(() => expect(useStore.getState().assistantSending).toBe(false))
    expect(within(aside).getByRole('button', { name: 'Remove check lint from the message' })).toBeDefined()
    expect(box(aside).value).toBe('Why is this stuck?')
  })

  it('sends nothing and shows the error when the server cannot forget a removed page', async () => {
    vi.mocked(api.assistantView).mockImplementation(async (context) => {
      if (!context.route) throw new Error('connection reset')
      return {}
    })
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    await userEvent.click(within(aside).getByRole('button', { name: 'Remove page Fix login test from the message' }))
    await userEvent.type(box(aside), '{Enter}')
    expect(await within(aside).findByRole('alert')).toHaveProperty('textContent', 'assistant.view: connection reset')
    expect(api.assistantPrompt).not.toHaveBeenCalled()
    vi.mocked(api.assistantView).mockImplementation(async () => ({}))
  })

  it('drops a removed attachment from the payload and keeps the member\'s own draft', async () => {
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'my own words')
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    expect(box(aside).value).toBe('my own words')
    await userEvent.click(within(aside).getByRole('button', { name: 'Remove check lint from the message' }))
    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()?.attachments).toBeUndefined()
    expect(sent()?.route).toMatchObject({ run_id: login.id })
  })

  it('sends attachments, and nothing of the page, while sharing is off', async () => {
    vi.mocked(api.assistantStatus).mockResolvedValue(status({ share_view: false }))
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await within(aside).findByRole('button', { name: /Page not shared/ })
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()).toMatchObject({ attachments: [{ kind: 'check', check: 'lint' }] })
    expect(sent()?.route).toBeUndefined()
  })
})

describe('inspecting what is shared', () => {
  it('lists and shows as raw JSON exactly what the next message sends', async () => {
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    await userEvent.click(within(aside).getByRole('button', { name: 'Inspect what is shared' }))
    const shared = await screen.findByRole('dialog', { name: 'What is shared' })
    const list = within(shared).getByRole('region', { name: 'Sent with the next message' })
    expect(list.textContent).toContain('Fix login test')
    expect(list.textContent).toContain('acme/api#42 · check lint')
    await userEvent.click(within(shared).getByRole('button', { name: 'Raw JSON' }))
    const raw = JSON.parse(within(shared).getByLabelText('Raw JSON').textContent!) as ViewContext
    await userEvent.keyboard('{Escape}')

    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()).toEqual(raw)
  })

  it('sends the remembered launch choices with the page, newest first, and only with it', async () => {
    useStore.setState({ launchDefaults: { codex: { mode: 'tui', at: 1 }, claude: { mode: 'acp', model: 'opus', at: 2 }, 'Bad Name': { mode: 'acp', at: 3 } } })
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await userEvent.click(within(aside).getByRole('button', { name: 'Inspect what is shared' }))
    const shared = await screen.findByRole('dialog', { name: 'What is shared' })
    expect(within(shared).getByRole('region', { name: 'Sent with the next message' }).textContent).toContain('claude opus Enhanced, codex Standard')
    await userEvent.keyboard('{Escape}')
    await userEvent.type(box(aside), 'Build the export{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()?.launch_defaults).toEqual([{ agent: 'claude', mode: 'acp', model: 'opus' }, { agent: 'codex', mode: 'tui' }])

    await userEvent.click(within(aside).getByRole('button', { name: /^Remove page / }))
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(2))
    expect(sent(1)).toMatchObject({ attachments: [{ kind: 'check', check: 'lint' }] })
    expect(sent(1)?.launch_defaults).toBeUndefined()
  })

  it('stops sharing the page from the same place', async () => {
    vi.mocked(api.assistantSettings).mockResolvedValue({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: false })
    const aside = await openPanel()
    await userEvent.click(within(aside).getByRole('button', { name: 'Inspect what is shared' }))
    const shared = await screen.findByRole('dialog', { name: 'What is shared' })
    await userEvent.click(within(shared).getByRole('checkbox', { name: 'Share the page I am on with the assistant' }))
    expect(api.assistantSettings).toHaveBeenCalledWith({ share_view: false })
    expect(within(shared).getByText(/Nothing is sent/)).toBeDefined()
    await userEvent.keyboard('{Escape}')
    await userEvent.type(box(aside), 'hello{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()).toBeUndefined()
  })
})

describe('the composer', () => {
  it('sends one prompt for a double Enter or a double click', async () => {
    let finish!: () => void
    vi.mocked(api.assistantPrompt).mockImplementation(() => new Promise((resolve) => { finish = () => resolve({}) }))
    const aside = await openPanel()
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'once')
    fireEvent.keyDown(box(aside), { key: 'Enter' })
    fireEvent.keyDown(box(aside), { key: 'Enter' })
    fireEvent.click(within(aside).getByRole('button', { name: /^Send|Sending/ }))
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    await act(async () => finish())
    await waitFor(() => expect(box(aside).value).toBe(''))
    expect(api.assistantPrompt).toHaveBeenCalledTimes(1)
  })

  it('sends nothing the member removed while the prompt was being keyed', async () => {
    let keyed!: () => void
    const digest = crypto.subtle.digest.bind(crypto.subtle)
    vi.spyOn(crypto.subtle, 'digest').mockImplementationOnce((algorithm, data) => new Promise((resolve) => { keyed = () => resolve(digest(algorithm, data)) }))
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    await userEvent.click(screen.getByRole('button', { name: 'Fix lint with the assistant' }))
    fireEvent.keyDown(box(aside), { key: 'Enter' })
    await userEvent.click(within(aside).getByRole('button', { name: 'Remove check lint from the message' }))
    await act(async () => keyed())
    expect(api.assistantPrompt).not.toHaveBeenCalled()

    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()?.attachments).toBeUndefined()
  })

  it('cancels once for a double click on Stop, and lets the member stop again if the agent ignores it', async () => {
    let cancelled!: () => void
    vi.mocked(api.assistantCancel).mockImplementation(() => new Promise((resolve) => { cancelled = () => resolve({}) }))
    render(<AppShell />)
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    const aside = await screen.findByRole('complementary', { name: 'Assistant' })
    assistantSocket().open({ state: state({ turn_in_flight: true }) }, [item('turn_start', 1), say(1, 'user', 'Install Rust')])
    const stop = await within(aside).findByRole('button', { name: 'Stop' })
    fireEvent.click(stop)
    fireEvent.click(stop)
    expect(within(aside).getByRole('button', { name: 'Stopping…' })).toHaveProperty('disabled', true)
    expect(api.assistantCancel).toHaveBeenCalledTimes(1)

    await act(async () => cancelled())
    await userEvent.click(await within(aside).findByRole('button', { name: 'Stop' }))
    expect(api.assistantCancel).toHaveBeenCalledTimes(2)
  })

  it('keeps a send error across closing the panel and clears it on edit', async () => {
    vi.mocked(api.assistantPrompt).mockRejectedValueOnce(new Error('assistant is not running'))
    const aside = await openPanel()
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'hi{Enter}')
    await within(aside).findByText('assistant is not running')
    await userEvent.click(within(aside).getByRole('button', { name: 'Close assistant' }))
    await userEvent.click(screen.getByRole('button', { name: 'Assistant' }))
    const reopened = await screen.findByRole('complementary', { name: 'Assistant' })
    expect(within(reopened).getByText('assistant is not running')).toBeDefined()
    await userEvent.type(box(reopened), '!')
    expect(within(reopened).queryByText('assistant is not running')).toBeNull()
  })

  it('offers the run page prompts that follow its state, and review only with an open pull request', async () => {
    const examples = (aside: HTMLElement) => within(within(aside).getByRole('region', { name: 'Suggestions' })).getAllByRole('button').map((b) => b.textContent)
    const aside = await openPanel()
    await within(aside).findByRole('region', { name: 'Suggestions' })
    expect(examples(aside)).not.toContain('Fix this')
    expect(examples(aside)).not.toContain('Why is this stuck?')
    expect(examples(aside)).not.toContain('Review and merge when checks pass')

    act(() => useStore.setState({ runs: runRecords({ ...login, status: 'failed' }, docs) }))
    expect(examples(aside)[0]).toBe('Fix this')
    act(() => useStore.setState({ runs: runRecords({ ...login, status: 'running', state: 'waiting-ci' }, docs) }))
    expect(examples(aside)[0]).toBe('Why is this stuck?')
    expect(examples(aside)).not.toContain('Fix this')

    vi.mocked(api.runPRFeedback).mockResolvedValue(feedback)
    await act(() => trackPullRequest(useStore.getState().runs[login.id]!, { repository: 'acme/api', base_branch: 'main', head_repository: 'acme/api', head_branch: 'fix' }))
    expect(examples(aside)).toContain('Review and merge when checks pass')
    await userEvent.click(within(aside).getByRole('button', { name: 'Why is this stuck?' }))
    expect(box(aside).value).toBe('Why is this stuck?')
  })

  it('attaches the run a suggestion is about, so it reaches the agent with page sharing off', async () => {
    vi.mocked(api.assistantStatus).mockResolvedValue(status({ share_view: false }))
    useStore.setState({ runs: runRecords({ ...login, status: 'failed' }, docs) })
    const aside = await openPanel()
    await userEvent.click(await within(aside).findByRole('button', { name: 'Fix this' }))
    expect(box(aside).value).toBe('Fix this')
    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()).toMatchObject({ attachments: [{ kind: 'run', workspace_id: workspace.id, run_id: login.id }] })
    expect(sent()?.route).toBeUndefined()
  })

  it('takes focus for an entry point once, not when the composer mounts again', async () => {
    const aside = await openPanel(<CheckPills feedback={feedback} target={{ runId: login.id, workspaceId: workspace.id }} />)
    const pill = screen.getByRole('button', { name: 'Fix lint with the assistant' })
    await userEvent.click(pill)
    await waitFor(() => expect(document.activeElement).toBe(box(aside)))
    pill.focus()
    act(() => useStore.getState().setAssistantStatus(status({ state: 'failed' })))
    act(() => useStore.getState().setAssistantStatus(status()))
    await within(aside).findByRole('textbox', { name: 'Message the assistant' })
    await new Promise((resolve) => requestAnimationFrame(resolve))
    expect(document.activeElement).toBe(pill)
  })

  it('says which attachments it dropped past the cap', async () => {
    const aside = await openPanel()
    const runs = Array.from({ length: 7 }, (_, i) => ({ kind: 'run' as const, workspace_id: workspace.id, run_id: `run-${i}`, label: `run-${i}` }))
    act(() => useStore.getState().askAssistant({ draft: 'compare these', attachments: runs }))
    expect(within(aside).getByRole('alert').textContent).toBe('A message carries at most 6 attachments; removed run run-0.')
    expect(useStore.getState().assistantAttachments.map((a) => a.run_id)).toEqual(runs.slice(1).map((r) => r.run_id))
  })

  it('clears a send error when the conversation restarts', () => {
    useStore.getState().setAssistantError('assistant is not running')
    useStore.getState().restartAssistantConversation()
    expect(useStore.getState().assistantError).toBeNull()
  })
})

describe('one chip per target', () => {
  it('shows a run attached on its own page once, and still sends both', async () => {
    const aside = await openPanel()
    act(() => useStore.getState().askAssistant({ draft: 'Fix this', attachments: [{ kind: 'run', workspace_id: workspace.id, run_id: login.id, label: login.id }] }))
    const chips = within(aside).getByRole('list', { name: 'Sent with this message' })
    expect(within(chips).getAllByText('Fix login test')).toHaveLength(1)
    expect(within(chips).getByRole('button', { name: 'Remove run Fix login test from the message' })).toBeDefined()
    await userEvent.click(within(aside).getByRole('button', { name: /^Send/ }))
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()).toMatchObject({ route: { run_id: login.id }, attachments: [{ kind: 'run', run_id: login.id }] })
  })

  it('removes the page with the one chip that stood for it', async () => {
    const aside = await openPanel()
    act(() => useStore.getState().askAssistant({ draft: 'Fix this', attachments: [{ kind: 'run', workspace_id: workspace.id, run_id: login.id, label: login.id }] }))
    const chips = within(aside).getByRole('list', { name: 'Sent with this message' })
    await userEvent.click(within(chips).getByRole('button', { name: 'Remove run Fix login test from the message' }))
    expect(within(chips).queryByText('Fix login test')).toBeNull()
    expect(within(aside).getByRole('button', { name: 'Add this page' })).toBeDefined()
    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(sent()).toBeUndefined()
  })

  it('never shows a check name the server would drop', async () => {
    const aside = await openPanel()
    act(() => useStore.getState().askAssistant({ draft: 'Fix this', attachments: [{ kind: 'check', run_id: login.id, check: 'li\u200bnt', label: 'lint' }] }))
    await userEvent.click(within(aside).getByRole('button', { name: 'Inspect what is shared' }))
    const shared = await screen.findByRole('dialog', { name: 'What is shared' })
    await userEvent.click(within(shared).getByRole('button', { name: 'Raw JSON' }))
    expect(within(shared).getByLabelText('Raw JSON').textContent).not.toContain('check":')
  })
})

describe('sharing changed under a failed send', () => {
  it('drops the retained context, so the retry sends what the chips show', async () => {
    vi.mocked(api.assistantPrompt).mockRejectedValueOnce(new Error('connection lost'))
    vi.mocked(api.assistantSettings).mockResolvedValue({ auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: false })
    const aside = await openPanel()
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'hi{Enter}')
    await within(aside).findByText('connection lost')
    expect(sent(0)?.route).toMatchObject({ run_id: login.id })

    await userEvent.click(within(aside).getByRole('button', { name: 'Inspect what is shared' }))
    const shared = await screen.findByRole('dialog', { name: 'What is shared' })
    await userEvent.click(within(shared).getByRole('checkbox', { name: 'Share the page I am on with the assistant' }))
    expect(within(shared).getByText(/Nothing is sent/)).toBeDefined()
    await userEvent.keyboard('{Escape}')
    expect(within(within(aside).getByRole('list', { name: 'Sent with this message' })).queryByText('Fix login test')).toBeNull()

    await userEvent.type(box(aside), '{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(2))
    expect(sent(1)).toBeUndefined()
  })
})

describe('the idempotency key', () => {
  const asOtherTab = () => localStorage.setItem('aether.assistant.sent', JSON.stringify({ ...JSON.parse(localStorage.getItem('aether.assistant.sent')!), tab: 'other-tab' }))

  it('is shared with another tab only while its send of the same prompt is in flight, never reused within one tab', async () => {
    const first = await promptKey('same draft', 1000)
    expect(await promptKey('same draft', 1001)).not.toBe(first)
    const held = JSON.parse(localStorage.getItem('aether.assistant.sent')!) as { key: string }
    asOtherTab()
    expect(await promptKey('same draft', 2000)).toBe(held.key)
    asOtherTab()
    expect(await promptKey('another draft', 2000)).not.toBe(held.key)
    asOtherTab()
    expect(await promptKey('same draft', 1001 + duplicateSendWindow)).not.toBe(held.key)
    expect(localStorage.getItem('aether.assistant.sent')).not.toContain('same draft')
  })

  it('is new for a second "yes" once the other tab\'s first one settled', async () => {
    const yes = await promptKey('yes', 1000)
    asOtherTab()
    settlePromptKey(yes)
    expect(await promptKey('yes', 2000)).not.toBe(yes)
  })

  it('is settled by the composer when its send ends', async () => {
    const aside = await openPanel()
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'yes{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(localStorage.getItem('aether.assistant.sent')).toBeNull())
  })
})

describe('without a secure context', () => {
  it('loads and sends over plain HTTP, where crypto.randomUUID and crypto.subtle are missing', async () => {
    vi.stubGlobal('crypto', { getRandomValues: crypto.getRandomValues.bind(crypto) })
    vi.resetModules()
    const fresh = await import('@/store/assistant')
    const key = await fresh.promptKey('hi')
    expect(key).toMatch(/^assistant-/)
    expect(localStorage.getItem('aether.assistant.sent')).toBeNull()

    useStore.setState({ route: { name: 'board', params: {} } })
    const aside = await openPanel()
    await userEvent.type(await within(aside).findByRole('textbox', { name: 'Message the assistant' }), 'hi{Enter}')
    await waitFor(() => expect(api.assistantPrompt).toHaveBeenCalledTimes(1))
    expect(vi.mocked(api.assistantPrompt).mock.calls[0]![1]).toMatch(/^assistant-/)
  })
})
