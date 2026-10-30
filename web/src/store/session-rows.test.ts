import type { SessionItem } from '@/lib/session-types'
import { appendItems, liveActivity, prependItems, rehome, rowsOfTurn, trimTurns, turnRows, type SessionRow, type Turn } from '@/store/session-rows'
import { indexItems, noAgents } from '@/store/subagents'
import { item, resetItems, say, tool } from '@/test/acp-stream'

beforeEach(resetItems)

const turnOf = (items: SessionItem[], closed = true): Turn => ({ turn: items[0]?.turn ?? 1, items, closed })
const kinds = (turn: Turn) => turnRows(turn).map((row) => row.kind)
const entries = (rows: SessionRow[]) => rows.flatMap((row) => (row.kind === 'steps' ? row.steps.flatMap((step) => (step.kind === 'tool' ? [step.entry] : step.kind === 'lookup' ? step.entries : [])) : []))

describe('session rows from the item log', () => {
  it('builds one turn: the person, one block of steps, and the answer with the outcome under it', () => {
    const rows = turnRows(turnOf([
      item('turn_start', 1),
      say(1, 'user', 'round the totals'),
      item('thought', 1, { message: { role: 'assistant', message_id: 't1', text: 'read first', complete: true } }),
      item('plan', 1, { plan: [{ content: 'Read', status: 'in_progress' }] }),
      tool(1, 'r1', 'read', 'Read src/a.js', 'in_progress', { locations: [{ path: 'src/a.js' }] }),
      tool(1, 'r1', 'read', 'Read src/a.js', 'completed', { locations: [{ path: 'src/a.js' }] }),
      tool(1, 'x1', 'execute', 'npm test', 'completed', { output: 'ok\n', exit_code: 0 }),
      tool(1, 'e1', 'edit', 'Edit src/a.js', 'completed', { diffs: [{ path: 'src/a.js', patch: '--- a/src/a.js\n+++ b/src/a.js\n@@ -1 +1 @@\n-a\n+b\n' }] }),
      item('plan', 1, { plan: [{ content: 'Read', status: 'completed' }] }),
      say(1, 'assistant', 'Done.'),
      item('turn_end', 1, { stop_reason: 'end_turn' }),
    ]))
    expect(rows.map((row) => row.kind)).toEqual(['user', 'steps', 'assistant'])
    expect(rows[1]).toMatchObject({
      id: 'steps:1:thought:1:t1',
      summary: 'Read 1 file, ran 1 command and edited 1 file',
      icons: ['think', 'plan', 'read'],
      plan: { done: 1, total: 1 },
      failed: 0,
      current: false,
      steps: [
        { kind: 'thought', text: 'read first' },
        { kind: 'plan', entries: [{ content: 'Read', status: 'completed' }] },
        { kind: 'lookup', entries: [{ label: 'Read src/a.js', target: 'src/a.js', status: 'done', durationMs: 1000 }] },
        { kind: 'tool', entry: { label: 'Ran npm test', command: 'npm test', output: 'ok\n', exitCode: 0 } },
        { kind: 'tool', entry: { label: 'Edited src/a.js' } },
      ],
    })
    expect(rows[2]).toMatchObject({ text: 'Done.', outcome: { text: 'Finished in 10s', tone: 'done' } })
  })

  it('appends streamed message segments and command output to the first row they opened', () => {
    const rows = turnRows(turnOf([
      item('turn_start', 1),
      item('message', 1, { message: { role: 'assistant', message_id: 'a', text: 'Hel' } }),
      tool(1, 'x', 'execute', 'make', 'in_progress', { output: 'one\n' }),
      item('message', 1, { message: { role: 'assistant', message_id: 'a', text: 'lo', complete: true } }),
      tool(1, 'x', 'execute', 'make', 'failed', { output: 'two\n', exit_code: 2 }),
    ], false))
    expect(rows).toMatchObject([
      { kind: 'assistant', text: 'Hello', streaming: false },
      { kind: 'steps', current: true, failed: 1, steps: [{ kind: 'tool', entry: { output: 'one\ntwo\n', status: 'failed', exitCode: 2 } }] },
    ])
  })

  it('keeps the agent prose in the conversation and blocks only the work between it', () => {
    const start = [item('turn_start', 1), say(1, 'user', 'go'), tool(1, 'x', 'execute', 'make', 'completed'), say(1, 'assistant', 'Built.')]
    expect(kinds(turnOf(start, false))).toEqual(['user', 'steps', 'assistant'])
    const more = turnRows(turnOf([...start, tool(1, 'y', 'execute', 'make test', 'in_progress')], false))
    expect(more).toMatchObject([
      { kind: 'user' },
      { kind: 'steps', id: 'steps:1:1:x', current: false, steps: [{ kind: 'tool', entry: { command: 'make' } }] },
      { kind: 'assistant', text: 'Built.' },
      { kind: 'steps', id: 'steps:1:1:y', current: true, running: 1, steps: [{ kind: 'tool', entry: { command: 'make test' } }] },
    ])
    expect(kinds(turnOf([item('turn_start', 1), say(1, 'user', 'ping'), say(1, 'assistant', 'pong'), item('turn_end', 1, { stop_reason: 'end_turn' })]))).toEqual(['user', 'assistant'])
  })

  it('keeps the reply last when the agent reports after it', () => {
    const end = item('turn_end', 1, { stop_reason: 'end_turn' })
    const rows = turnRows(turnOf([
      item('turn_start', 1),
      say(1, 'user', 'go'),
      tool(1, 'x', 'execute', 'make', 'completed'),
      say(1, 'assistant', 'Built.'),
      tool(1, 'r', 'execute', 'cd /workspace && /usr/local/bin/aether-internal report --outcome answered --summary "Built"', 'completed'),
      end,
    ]))
    expect(rows).toMatchObject([
      { kind: 'user' },
      { kind: 'steps', summary: 'Ran 2 commands', steps: [{ kind: 'tool', entry: { command: 'make' } }, { kind: 'tool', entry: { id: '1:r' } }] },
      { kind: 'assistant', text: 'Built.', outcome: { tone: 'done' } },
    ])
    const mcp = turnRows(turnOf([
      item('turn_start', 1),
      say(1, 'user', 'ping'),
      say(1, 'assistant', 'pong'),
      tool(1, 'r', 'other', 'aether_report', 'completed', { tool_name: 'mcp__aether__aether_report' }),
      end,
    ]))
    expect(mcp).toMatchObject([{ kind: 'user' }, { kind: 'steps', steps: [{ kind: 'tool', entry: { name: 'mcp__aether__aether_report' } }] }, { kind: 'assistant', text: 'pong', outcome: { tone: 'done' } }])
    const thought = turnRows(turnOf([
      item('turn_start', 1),
      say(1, 'user', 'go'),
      tool(1, 'x', 'execute', 'make', 'completed'),
      say(1, 'assistant', 'Built.'),
      item('thought', 1, { message: { role: 'assistant', message_id: 't', text: 'report it', complete: true } }),
      tool(1, 'r', 'execute', 'aether-internal report --outcome answered --summary "Built"', 'completed'),
      end,
    ]))
    expect(thought).toMatchObject([
      { kind: 'user' },
      { kind: 'steps', steps: [{ kind: 'tool' }, { kind: 'thought', text: 'report it' }, { kind: 'tool', entry: { id: '1:r' } }] },
      { kind: 'assistant', text: 'Built.', outcome: { tone: 'done' } },
    ])
  })

  it('starts a segment at each message from a person, each with its own block', () => {
    const rows = turnRows(turnOf([
      item('turn_start', 1),
      say(1, 'user', 'first', 'u1'),
      tool(1, 'x', 'execute', 'make', 'completed'),
      say(1, 'user', 'and docs', 'u2'),
      tool(1, 'y', 'execute', 'make docs', 'completed'),
      say(1, 'assistant', 'Both built.'),
      item('turn_end', 1, { stop_reason: 'cancelled' }),
    ]))
    expect(rows.map((row) => [row.kind, row.id])).toEqual([
      ['user', 'message:1:u1'],
      ['steps', 'steps:1:1:x'],
      ['user', 'message:1:u2'],
      ['steps', 'steps:1:1:y'],
      ['assistant', 'message:1:m6'],
      ['finished', 'end:1'],
    ])
  })

  it('keeps a plan update with the segment it came in, so a steered turn shows its new plan in the current block', () => {
    const rows = turnRows(turnOf([
      item('turn_start', 1),
      say(1, 'user', 'build it', 'u1'),
      item('plan', 1, { plan: [{ content: 'Build', status: 'in_progress' }] }),
      item('plan', 1, { plan: [{ content: 'Build', status: 'completed' }] }),
      say(1, 'user', 'and docs', 'u2'),
      item('plan', 1, { plan: [{ content: 'Build', status: 'completed' }, { content: 'Docs', status: 'in_progress' }] }),
    ], false))
    expect(rows).toMatchObject([
      { kind: 'user' },
      { kind: 'steps', current: false, plan: { done: 1, total: 1 }, steps: [{ kind: 'plan', entries: [{ content: 'Build', status: 'completed' }] }] },
      { kind: 'user' },
      { kind: 'steps', current: true, plan: { done: 1, total: 2 }, steps: [{ kind: 'plan', entries: [{ content: 'Build' }, { content: 'Docs', status: 'in_progress' }] }] },
    ])
  })

  it('groups consecutive reads, searches and fetches into one lookup step', () => {
    const [block] = turnRows(turnOf([
      tool(1, 'r1', 'read', 'Read a.go', 'completed', { locations: [{ path: 'src/a.go' }] }),
      tool(1, 's1', 'search', 'grep total', 'completed'),
      tool(1, 'f1', 'fetch', 'Fetch docs', 'in_progress', { raw_input: { url: 'https://example.com/docs' } }),
      tool(1, 'x1', 'execute', 'make', 'completed'),
      tool(1, 'r2', 'read', 'Read b.go', 'completed', { locations: [{ path: 'src/b.go' }] }),
      tool(1, 'm1', 'other', 'lookup issue 7', 'completed', { tool_name: 'mcp__tracker__get', raw_input: { id: 7 }, raw_output: { title: 'Crash' } }),
    ], false))
    expect(block).toMatchObject({ kind: 'steps', steps: [
      { kind: 'lookup', entries: [{ target: 'src/a.go' }, { target: 'grep total' }, { target: 'https://example.com/docs', status: 'running' }] },
      { kind: 'tool', entry: { command: 'make' } },
      { kind: 'lookup', entries: [{ target: 'src/b.go' }] },
      { kind: 'tool', entry: { name: 'mcp__tracker__get', target: 'lookup issue 7', input: { id: 7 }, result: { title: 'Crash' } } },
    ] })
  })

  it('leaves an answered request as a collapsed row and never shows a pending one in the timeline', () => {
    const request = { id: 'q', kind: 'permission' as const, title: 'rm -rf build', tool_call_id: 'x', options: [{ id: 'ok', name: 'Allow', kind: 'allow_once' }] }
    const turn = [item('turn_start', 1), tool(1, 'x', 'execute', 'rm -rf build', 'pending'), item('request', 1, { request: { ...request, status: 'pending' } })]
    expect(turnRows(turnOf(turn, false))).toMatchObject([{ kind: 'steps', steps: [{ kind: 'tool', entry: { waiting: true } }] }])
    const answered = turnRows(turnOf([...turn, item('request', 1, { request: { ...request, status: 'answered', answer: 'ok' } })], false))
    expect(answered).toMatchObject([{ kind: 'steps', steps: [{ kind: 'tool' }, { kind: 'answered', command: 'rm -rf build', request: { status: 'answered', answer: 'ok' } }] }])
  })

  it('turns the inbox wake and the session notices into event rows', () => {
    const rows = turnRows(turnOf([
      say(2, 'user', 'Aether has 2 unacknowledged inbox item(s). Run /usr/local/bin/aether-internal inbox, handle the batch.'),
      item('notice', 2, { notice: { severity: 'error', title: 'Turn interrupted', description: 'The agent connection ended.' } }),
      item('reset', 2),
      item('turn_end', 2, { stop_reason: 'cancelled' }),
    ]))
    expect(rows).toMatchObject([
      { kind: 'event', text: 'Woken by new agent messages' },
      { kind: 'event', text: 'Turn interrupted', detail: 'The agent connection ended.', tone: 'failed' },
      { kind: 'event', text: 'New agent conversation' },
      { kind: 'finished', text: 'Cancelled', tone: 'paused' },
    ])
  })

  it('keeps a prompt\'s room images and attached files apart and ignores any other link', () => {
    const file = `/home/alice/.aether/terminal-files/${'a'.repeat(32)}/Q3 report.pdf`
    const [row] = turnRows(turnOf([item('message', 1, { message: { role: 'user', message_id: 'u', text: 'read these', complete: true, attachments: [
      { type: 'image', mime_type: 'image/png', uri: 'aether://room/m1/0' },
      { type: 'resource_link', uri: 'file:///home/alice/.aether/terminal-files/' + 'a'.repeat(32) + '/Q3%20report.pdf' },
      { type: 'resource_link', uri: 'file:///home/alice/secrets.txt' },
      { type: 'resource_link', uri: 'https://example.com/a.pdf' },
    ] } })]))
    expect(row).toEqual(expect.objectContaining({ kind: 'user', images: [{ messageID: 'm1', index: 0 }], files: [file] }))
  })

  it('names the running tool, a streaming thought, or plain work as the live row', () => {
    const open = (items: SessionItem[]) => liveActivity(turnOf([item('turn_start', 1), ...items], false))?.label
    expect(open([tool(1, 'r', 'read', 'Read a.go', 'in_progress', { locations: [{ path: 'a.go' }] })])).toBe('Reading a.go')
    expect(open([item('thought', 1, { message: { role: 'assistant', message_id: 't', text: 'hm' } })])).toBe('Thinking')
    expect(open([say(1, 'user', 'go')])).toBe('Working')
    expect(liveActivity(turnOf([item('turn_start', 1), item('turn_end', 1)]))).toBeNull()
  })

  it('says a turn with only running subagent launches waits on them', () => {
    const agent = (id: string) => tool(1, id, 'other', `Agent ${id}`, 'in_progress', { tool_name: 'Agent' })
    const open = (items: SessionItem[]) => liveActivity(turnOf([item('turn_start', 1), ...items], false))?.label
    expect(open([agent('a1'), agent('a2')])).toBe('Waiting on 2 subagents')
    expect(open([agent('a1'), agent('a2'), tool(1, 'r', 'read', 'Read a.go', 'in_progress', { locations: [{ path: 'a.go' }] })])).toBe('Reading a.go')
  })

  it('says a tool call waits for approval while its permission is pending', () => {
    const asked = { id: 'p1', kind: 'permission' as const, title: 'rm -rf build', status: 'pending' as const, tool_call_id: 'x1' }
    const call = tool(1, 'x1', 'execute', 'rm -rf build', 'pending')
    const waiting = liveActivity(turnOf([item('turn_start', 1), call, item('request', 1, { request: asked })], false))
    expect(waiting).toEqual({ label: 'Waiting for your approval: rm -rf build', tone: 'needs-you' })
    const answered = liveActivity(turnOf([item('turn_start', 1), call, item('request', 1, { request: asked }), item('request', 1, { request: { ...asked, status: 'answered', answer: 'allow' } })], false))
    expect(answered).toEqual({ label: 'Running rm -rf build', tone: 'working' })
    const question = { id: 'q1', kind: 'question' as const, title: 'Which database?', status: 'pending' as const }
    expect(liveActivity(turnOf([item('turn_start', 1), item('request', 1, { request: question })], false))).toEqual({ label: 'Waiting for your answer', tone: 'needs-you' })
  })
})

describe('autonomous turns and background tasks', () => {
  const prompt = [item('turn_start', 1), item('turn_end', 1, { stop_reason: 'end_turn' })]

  it('opens a turn for an autonomous turn_start, names its work and closes it on turn_end', () => {
    const turns = appendItems([], [...prompt, item('turn_start', 2, { autonomous: true })])
    expect(turns.map((t) => [t.turn, t.closed])).toEqual([[1, true], [2, false]])
    expect(liveActivity(turns.at(-1))).toEqual({ label: 'Working', tone: 'working' })
    const working = appendItems(turns, [tool(2, 'x', 'execute', 'make', 'in_progress')])
    expect(liveActivity(working.at(-1))?.label).toBe('Running make')
    const ended = appendItems(working, [item('turn_end', 2, { autonomous: true, origin: 'task-notification', stop_reason: 'end_turn' })])
    expect(liveActivity(ended.at(-1))).toBeNull()
    expect(rowsOfTurn(ended.at(-1)!).at(-1)).toMatchObject({ kind: 'finished', text: 'Finished in 2s', tone: 'done' })
  })

  it('words a cancelled turn as Cancelled and one cut off by a restart as Interrupted', () => {
    const finish = (stop_reason: string) => turnRows(turnOf([item('turn_start', 1), item('turn_end', 1, { stop_reason })])).at(-1)
    expect(finish('cancelled')).toMatchObject({ kind: 'finished', text: 'Cancelled in 1s', tone: 'paused' })
    expect(finish('interrupted')).toMatchObject({ kind: 'finished', text: 'Interrupted', tone: 'failed' })
  })
})

describe('turns', () => {
  it('freezes a closed turn: its rows are derived once and kept', () => {
    const turns = appendItems([], [item('turn_start', 1), say(1, 'user', 'hi'), item('turn_end', 1, { stop_reason: 'end_turn' }), item('turn_start', 2)])
    const closed = turns[0]!
    expect(closed.closed).toBe(true)
    expect(rowsOfTurn(closed)).toBe(rowsOfTurn(closed))
    const next = appendItems(turns, [say(2, 'user', 'again')])
    expect(next[0]).toBe(closed)
    expect(next[1]).not.toBe(turns[1])
    expect(next[1]!.items).toHaveLength(2)
    expect(turns[1]!.items).toHaveLength(1)
  })

  it('pages older items in front, joining the turn they share', () => {
    const older = [item('turn_start', 1), say(1, 'user', 'a')]
    const held = appendItems([], [say(1, 'assistant', 'b'), item('turn_end', 1)])
    const turns = prependItems(held, older)
    expect(turns).toHaveLength(1)
    expect(turns[0]!.items.map((it) => it.seq)).toEqual([1, 2, 3, 4])
  })

  it('trims to the newest items', () => {
    const turns = appendItems([], Array.from({ length: 10 }, (_, i) => item('usage', 1 + Math.floor(i / 4))))
    expect(trimTurns(turns, 6).turns.flatMap((turn) => turn.items.map((it) => it.seq))).toEqual([6, 7, 8, 9, 10])
  })

  it('keeps row and entry ids unique when turns reuse a tool call or message id', () => {
    const turn = (n: number) => [
      item('turn_start', n),
      say(n, 'user', 'go', 'same'),
      tool(n, 'mock-tool', 'read', 'Read a.js', 'completed'),
      item('turn_end', n, { stop_reason: 'end_turn' }),
    ]
    const rows = appendItems([], [...turn(1), ...turn(2)]).flatMap(rowsOfTurn)
    const ids = rows.map((row) => row.id)
    expect(new Set(ids).size).toBe(ids.length)
    expect(entries(rows).map((entry) => entry.id)).toEqual(['1:mock-tool', '2:mock-tool'])
  })

  it('leaves out a duration too short to matter', () => {
    const started = tool(1, 'r1', 'read', 'Read a.js', 'in_progress')
    const done = { ...tool(1, 'r1', 'read', 'Read a.js', 'completed'), time: started.time }
    expect(entries(turnRows(turnOf([started, done])))).toMatchObject([{ status: 'done', durationMs: undefined }])
  })
})

describe('subagent rows', () => {
  const launch = (id: string, status = 'in_progress') =>
    tool(1, id, 'other', `Agent ${id}`, status, { tool_name: 'Agent', subagent: { description: id } })

  it('groups consecutive launches into one row that prose or a tool call breaks', () => {
    const rows = turnRows(turnOf([
      item('turn_start', 1),
      launch('a'), launch('b'), launch('a', 'completed'),
      say(1, 'assistant', 'waiting'),
      launch('c'),
      tool(1, 'r', 'read', 'Read x', 'completed'),
      launch('d'),
    ], false))
    expect(rows).toMatchObject([
      { kind: 'steps', summary: 'Delegated 2 tasks', agents: ['a', 'b'], steps: [{ id: 'agents:1:a', ids: ['a', 'b'] }] },
      { kind: 'assistant', text: 'waiting' },
      { kind: 'steps', summary: 'Delegated 2 tasks and read 1 file', agents: ['c', 'd'], steps: [{ id: 'agents:1:c', ids: ['c'] }, { kind: 'tool' }, { ids: ['d'] }] },
    ])
  })

  it('renders items without parent metadata exactly as before', () => {
    const rows = turnRows(turnOf([item('turn_start', 1), tool(1, 'r', 'read', 'Read x', 'completed'), tool(1, 'e', 'other', 'Delegate', 'completed', { tool_name: 'Edit' })]))
    expect(rows.map((row) => row.kind)).toEqual(['steps'])
    expect(indexItems(noAgents, [tool(1, 'r', 'read', 'Read x', 'completed')]).index.roots).toEqual([])
  })

  it('appends a snapshot logged in a later turn to the turn of the call, once', () => {
    const diff = { path: 'a.go', patch: '--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a\n+b\n' }
    const first = tool(1, 'e', 'edit', 'Edit a.go', 'in_progress', { diffs: [diff] })
    const items = [item('turn_start', 1), first, item('turn_end', 1), item('turn_start', 2), tool(2, 'e', 'edit', 'Edit a.go', 'completed', { diffs: [diff] })]
    const { main, home } = indexItems(noAgents, items)
    const turns = appendItems([], main, (it) => home.get(it))
    expect(turns.map((turn) => turn.items.length)).toEqual([4, 1])
    const rows = turns.flatMap(rowsOfTurn)
    expect(entries(rows)).toHaveLength(1)
  })

  it('keeps a carried call running while its snapshots arrive after its turn ended', () => {
    const items = [item('turn_start', 1), tool(1, 'x', 'execute', 'make', 'in_progress'), item('turn_end', 1), item('turn_start', 2), tool(2, 'x', 'execute', 'make', 'in_progress', { output: 'ok' })]
    const { main, home } = indexItems(noAgents, items)
    const [first] = appendItems([], main, (it) => home.get(it))
    expect(entries(rowsOfTurn(first!))).toMatchObject([{ status: 'running' }])
    expect(entries(rowsOfTurn({ ...first!, items: first!.items.slice(0, 3) }))).toMatchObject([{ status: 'done' }])
  })

  it('joins an older page with a held turn of the same number after a trim', () => {
    const early = [item('turn_start', 1), say(1, 'user', 'go'), item('turn_end', 1), item('turn_start', 2), say(2, 'user', 'again')]
    const late = tool(1, 'x', 'execute', 'make', 'completed')
    const held: Turn[] = [{ turn: 1, items: [late], closed: true }, { turn: 2, items: [say(2, 'assistant', 'ok')], closed: false }]
    const turns = prependItems(held, early)
    expect(turns.map((turn) => turn.turn)).toEqual([1, 2])
    expect(turns.map((turn) => turn.items.length)).toEqual([4, 3])
    expect(turns[0]!.items.at(-1)).toBe(late)
  })

  it('homes an older page snapshot into a held earlier turn the page does not reach', () => {
    const first = tool(1, 'x', 'execute', 'make', 'in_progress')
    const items = [item('turn_start', 1), first, item('turn_end', 1), item('turn_start', 2), tool(2, 'x', 'execute', 'make', 'in_progress', { output: 'a' }),
      say(2, 'user', 'more'), tool(2, 'x', 'execute', 'make', 'completed')]
    const all = indexItems(noAgents, items)
    const held = trimTurns(appendItems([], all.main, (it) => all.home.get(it)), 6).turns
    expect(held.map((turn) => turn.turn)).toEqual([1, 2])
    const page = indexItems(all.index, items.slice(3, 5), true)
    const turns = prependItems(held, page.main, (it) => page.home.get(it))
    expect(turns.map((turn) => turn.turn)).toEqual([1, 2])
    expect(turns[0]!.items.map((it) => it.seq)).toEqual([5, 7])
    expect(entries(turns.flatMap(rowsOfTurn))).toHaveLength(1)
  })

  it('keeps a settled call whose id comes back in a later turn as a new call', () => {
    const items = [item('turn_start', 1), tool(1, 't', 'read', 'Read a.js', 'completed'), item('turn_end', 1), item('turn_start', 2), tool(2, 't', 'read', 'Read b.js', 'completed')]
    const { main, home } = indexItems(noAgents, items)
    const turns = appendItems([], main, (it) => home.get(it))
    expect(turns.map((turn) => turn.items.length)).toEqual([3, 2])
    expect(entries(turns.flatMap(rowsOfTurn))).toHaveLength(2)
    const older = indexItems(indexItems(noAgents, items.slice(3)).index, items.slice(0, 3), true)
    expect(older.rehomed).toEqual([])
  })

  it('moves a snapshot to the turn an older page reveals as its home', () => {
    const early = tool(1, 'x', 'execute', 'make', 'in_progress')
    const end = item('turn_end', 1)
    const late = tool(2, 'x', 'execute', 'make', 'completed')
    const live = indexItems(noAgents, [late])
    let turns = appendItems([], live.main)
    const older = indexItems(live.index, [early, end], true)
    turns = rehome(prependItems(turns, older.main), older.rehomed)
    expect(turns.map((turn) => turn.items.map((it) => it.seq))).toEqual([[1, 2, 3], []])
    expect(rehome(turns, [])).toBe(turns)
    expect(rehome(turns, [{ id: 'x', from: 2, to: 9 }])).toBe(turns)
  })

  it('gives a tool call split across turns of one older page a single finished step', () => {
    const page = [item('turn_start', 1), tool(1, 'x', 'execute', 'make', 'in_progress'), item('turn_end', 1), item('turn_start', 2), tool(2, 'x', 'execute', 'make', 'completed')]
    const { main, home } = indexItems(noAgents, page, true)
    const turns = prependItems([], main, (it) => home.get(it))
    expect(entries(turns.flatMap(rowsOfTurn))).toMatchObject([{ status: 'done' }])
  })

  it('trims by seq across turns and keeps every segment of a retained user message', () => {
    const items = [item('turn_start', 1), tool(1, 'x', 'execute', 'make', 'in_progress'), item('turn_end', 1), item('turn_start', 2), say(2, 'user', 'a', 'm'), say(2, 'user', 'b', 'm'), tool(2, 'x', 'execute', 'make', 'completed')]
    const { main, home } = indexItems(noAgents, items)
    const turns = appendItems([], main, (it) => home.get(it))
    expect(turns.map((turn) => turn.items.map((it) => it.seq))).toEqual([[1, 2, 3, 7], [4, 5, 6]])
    const late = trimTurns(turns, 7)
    expect(late.floor).toBe(7)
    expect(late.turns.map((turn) => turn.items.map((it) => it.seq))).toEqual([[7]])
    const message = trimTurns(turns, 6)
    expect(message.floor).toBe(5)
    expect(message.turns.map((turn) => turn.items.map((it) => it.seq))).toEqual([[7], [5, 6]])
  })
})
