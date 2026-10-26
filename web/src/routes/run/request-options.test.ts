import type { SessionOption, SessionRequest, SessionToolCall } from '@/lib/session-types'
import { answerTone, answerWord, choicePlan, plainHeadline, requestHeadline } from '@/routes/run/request-options'

const claude: SessionOption[] = [
  { id: 'always', name: 'Always Allow', kind: 'allow_always' },
  { id: 'allow', name: 'Allow', kind: 'allow_once' },
  { id: 'reject', name: 'Reject', kind: 'reject_once' },
]

const permission = (options: SessionOption[], over: Partial<SessionRequest> = {}): SessionRequest =>
  ({ id: 'r1', kind: 'permission', title: 'rm -rf build', status: 'pending', options, ...over })

const call = (over: Partial<SessionToolCall>): SessionToolCall => ({ id: 'c1', title: 'Edit a.go', ...over })

const summary = (plan: ReturnType<typeof choicePlan>) => plan.kind === 'pills'
  ? { pills: [plan.once, plan.always].map((c) => `${c.option.id}:${c.digit}`), deny: plan.deny?.option.id, extras: plan.extras.map((c) => c.option.id) }
  : plan.choices.map((c) => `${c.option.id}:${c.digit ?? '-'}${c.primary ? '*' : ''}`)

describe('choicePlan', () => {
  it('offers How long pills for one allow_once and one allow_always, keeping digit 1 on allow_once', () => {
    expect(summary(choicePlan(permission(claude), call({ tool_kind: 'execute' })))).toEqual({ pills: ['allow:1', 'always:3'], deny: 'reject', extras: [] })
    const both = [...claude, { id: 'never', name: 'Always Reject', kind: 'reject_always' }]
    expect(summary(choicePlan(permission(both), call({ tool_kind: 'execute' })))).toEqual({ pills: ['allow:1', 'always:4'], deny: 'reject', extras: ['never'] })
  })

  it('keeps the agent\'s own buttons for a mode switch, an unknown call, empty kinds or repeated kinds, rejects first and the primary last', () => {
    expect(summary(choicePlan(permission(claude), call({ tool_kind: 'switch_mode' })))).toEqual(['reject:2', 'always:3', 'allow:1*'])
    expect(choicePlan(permission(claude), undefined).kind).toBe('buttons')
    const unnamed = [{ id: 'yes', name: 'Yes' }, { id: 'no', name: 'No', kind: 'reject_once' }]
    expect(summary(choicePlan(permission(unnamed), undefined))).toEqual(['no:2', 'yes:1*'])
    const repeated = [...claude, { id: 'allow2', name: 'Allow here', kind: 'allow_once' }]
    expect(choicePlan(permission(repeated), undefined).kind).toBe('buttons')
  })

  it('puts Decline before Accept on a question and never keys allow_always to 1', () => {
    const question: SessionRequest = { id: 'q', kind: 'question', title: 'Which database?', status: 'pending', options: [{ id: 'accept', name: 'Accept' }, { id: 'decline', name: 'Decline' }] }
    expect(summary(choicePlan(question, undefined))).toEqual(['decline:2', 'accept:1*'])
    expect(summary(choicePlan(permission([claude[0]!]), undefined))).toEqual(['always:-*'])
  })
})

describe('requestHeadline', () => {
  const headline = (request: SessionRequest, c?: SessionToolCall) => plainHeadline(requestHeadline(request, c, 'Claude Code'))

  it('names the action from the tool kind and its files', () => {
    expect(headline(permission(claude), call({ tool_kind: 'execute', title: 'rm -rf build' }))).toBe('Claude Code wants to run a command')
    expect(headline(permission(claude), call({ tool_kind: 'edit', diffs: [{ path: 'web/src/a.ts', patch: '' }] }))).toBe('Claude Code wants to edit a.ts')
    expect(headline(permission(claude), call({ tool_kind: 'delete', locations: [{ path: 'a.go' }, { path: 'b.go' }] }))).toBe('Claude Code wants to delete 2 files')
    expect(headline(permission(claude), call({ tool_kind: 'other', tool_name: 'mcp__github__create_issue' }))).toBe('Claude Code wants to use mcp__github__create_issue')
  })

  it('asks plainly when the tool call has not arrived, and for questions and links', () => {
    expect(headline(permission(claude))).toBe('Claude Code asks for permission')
    expect(headline({ ...permission([]), kind: 'question' })).toBe('Claude Code asks a question')
    expect(headline({ ...permission([]), kind: 'link' })).toBe('Claude Code asks you to open a link')
  })
})

describe('answer words', () => {
  it('reads the verdict from the chosen option kind', () => {
    const answered = (answer: string) => permission(claude, { status: 'answered', answer })
    expect([answerWord(answered('allow')), answerTone(answered('allow'))]).toEqual(['Allowed once', 'done'])
    expect([answerWord(answered('always')), answerTone(answered('always'))]).toEqual(['Always allowed', 'done'])
    expect([answerWord(answered('reject')), answerTone(answered('reject'))]).toEqual(['Denied', 'failed'])
    expect(answerWord(permission([{ id: 'yes', name: 'Yes' }], { status: 'answered', answer: 'yes' }))).toBe('Answered “Yes”')
    expect([answerWord(permission(claude, { status: 'cancelled' })), answerTone(permission(claude, { status: 'cancelled' }))]).toEqual(['Cancelled', 'neutral'])
  })
})
