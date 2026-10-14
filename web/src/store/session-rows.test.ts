import type { SessionItem } from '@/lib/session-types'
import { appendItems, backgroundTasks, liveActivity, prependItems, rowsOfTurn, trimTurns, turnRows, type Turn } from '@/store/session-rows'
import { item, resetItems, say, tool } from '@/test/acp-stream'

beforeEach(resetItems)

const turnOf = (items: SessionItem[], closed = true): Turn => ({ turn: items[0]?.turn ?? 1, items, closed })
const kinds = (turn: Turn) => turnRows(turn).map((row) => row.kind)

describe('session rows from the item log', () => {
  it('builds one turn: the person, the work folded, the plan, the answer, the files and the finish', () => {
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
    expect(rows.map((row) => row.kind)).toEqual(['user', 'thinking', 'plan', 'work', 'assistant', 'changed-files', 'finished'])
    expect(rows[2]).toMatchObject({ entries: [{ content: 'Read', status: 'completed' }] })
    expect(rows[3]).toMatchObject({
      summary: 'Read 1 file, ran 1 command and edited 1 file',
      entries: [
        { label: 'Read src/a.js', status: 'done', durationMs: 1000 },
        { label: 'Ran npm test', command: 'npm test', output: 'ok\n', exitCode: 0 },
        { label: 'Edited src/a.js' },
      ],
    })
    expect(rows[5]).toMatchObject({ files: [{ path: 'src/a.js', additions: 1, deletions: 1 }] })
    expect(rows[6]).toMatchObject({ text: 'Finished in 10s', tone: 'done' })
  })

  it('appends streamed message segments and command output to the first row they opened', () => {
    const rows = turnRows(turnOf([
      item('turn_start', 1),
      item('message', 1, { message: { role: 'assistant', message_id: 'a', text: 'Hel' } }),
      tool(1, 'x', 'execute', 'make', 'in_progress', { output: 'one\n' }),
      item('message', 1, { message: { role: 'assistant', message_id: 'a', text: 'lo', complete: true } }),
      tool(1, 'x', 'execute', 'make', 'failed', { output: 'two\n', exit_code: 2 }),
    ], false))
    expect(rows[0]).toMatchObject({ kind: 'assistant', text: 'Hello', streaming: false })
    expect(rows[1]).toMatchObject({ kind: 'work', entries: [{ output: 'one\ntwo\n', status: 'failed', exitCode: 2 }] })
  })

  it('leaves an answered request as a collapsed row and never shows a pending one in the timeline', () => {
    const request = { id: 'q', kind: 'permission' as const, title: 'rm -rf build', tool_call_id: 'x', options: [{ id: 'ok', name: 'Allow', kind: 'allow_once' }] }
    const turn = [item('turn_start', 1), tool(1, 'x', 'execute', 'rm -rf build', 'pending'), item('request', 1, { request: { ...request, status: 'pending' } })]
    expect(kinds(turnOf(turn, false))).toEqual(['work'])
    const answered = turnRows(turnOf([...turn, item('request', 1, { request: { ...request, status: 'answered', answer: 'ok' } })], false))
    expect(answered[1]).toMatchObject({ kind: 'answered', command: 'rm -rf build', request: { status: 'answered', answer: 'ok' } })
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
      { kind: 'event', text: 'New agent session' },
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

  it('says a tool call waits for approval while its permission is pending', () => {
    const asked = { id: 'p1', kind: 'permission' as const, title: 'rm -rf build', status: 'pending' as const, tool_call_id: 'x1' }
    const call = tool(1, 'x1', 'execute', 'rm -rf build', 'pending')
    const waiting = liveActivity(turnOf([item('turn_start', 1), call, item('request', 1, { request: asked })], false))
    expect(waiting).toEqual({ label: 'Waiting for your approval: rm -rf build', waiting: true })
    const answered = liveActivity(turnOf([item('turn_start', 1), call, item('request', 1, { request: asked }), item('request', 1, { request: { ...asked, status: 'answered', answer: 'allow' } })], false))
    expect(answered).toEqual({ label: 'Running rm -rf build', waiting: false })
    const question = { id: 'q1', kind: 'question' as const, title: 'Which database?', status: 'pending' as const }
    expect(liveActivity(turnOf([item('turn_start', 1), item('request', 1, { request: question })], false))).toEqual({ label: 'Waiting for your answer', waiting: true })
  })
})

describe('autonomous turns and background tasks', () => {
  const prompt = [item('turn_start', 1), item('turn_end', 1, { stop_reason: 'end_turn', background_tasks: ['bg1'] })]

  it('opens a turn for an autonomous turn_start, names its work and closes it on turn_end', () => {
    const turns = appendItems([], [...prompt, item('turn_start', 2, { autonomous: true })])
    expect(turns.map((t) => [t.turn, t.closed])).toEqual([[1, true], [2, false]])
    expect(liveActivity(turns.at(-1))).toEqual({ label: 'Working', waiting: false })
    const working = appendItems(turns, [tool(2, 'x', 'execute', 'make', 'in_progress')])
    expect(liveActivity(working.at(-1))?.label).toBe('Running make')
    const ended = appendItems(working, [item('turn_end', 2, { autonomous: true, origin: 'task-notification', stop_reason: 'end_turn' })])
    expect(liveActivity(ended.at(-1))).toBeNull()
    expect(rowsOfTurn(ended.at(-1)!).at(-1)).toMatchObject({ kind: 'finished', text: 'Finished in 2s', tone: 'done' })
  })

  it('reads the tasks off the newest turn boundary', () => {
    expect(backgroundTasks(appendItems([], prompt))).toEqual(['bg1'])
    expect(backgroundTasks(appendItems([], [...prompt, item('turn_start', 2, { autonomous: true })]))).toEqual([])
    expect(backgroundTasks(appendItems([], [item('usage', 1)]))).toEqual([])
    expect(backgroundTasks([])).toEqual([])
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
    expect(trimTurns(turns, 5).flatMap((turn) => turn.items.map((it) => it.seq))).toEqual([6, 7, 8, 9, 10])
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
    const entries = rows.flatMap((row) => (row.kind === 'work' ? row.entries.map((entry) => entry.id) : []))
    expect(entries).toEqual(['1:mock-tool', '2:mock-tool'])
  })

  it('leaves out a duration too short to matter', () => {
    const started = tool(1, 'r1', 'read', 'Read a.js', 'in_progress')
    const done = { ...tool(1, 'r1', 'read', 'Read a.js', 'completed'), time: started.time }
    const [work] = turnRows(turnOf([started, done]))
    expect(work).toMatchObject({ entries: [{ status: 'done', durationMs: undefined }] })
  })
})
