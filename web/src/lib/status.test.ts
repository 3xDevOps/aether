import { awaitingReview, runLabel, runState, waitsOnHuman } from '@/lib/status'
import { isArchivable, isTerminal } from '@/store/runs'

describe('runLabel', () => {
  it('prefers a terminal title over the task', () => {
    expect(runLabel({ title: 'Fixing the login bug', task: 'fix login' })).toBe(
      'Fixing the login bug',
    )
  })

  it('falls back to the task and then a placeholder', () => {
    expect(runLabel({ title: '', task: '  fix login  ' })).toBe('fix login')
    expect(runLabel({ task: '   ' })).toBe('Untitled run')
  })

  it('bounds an untitled run to the first line of its prompt', () => {
    expect(
      runLabel({ task: '\n  Goal: fix login.\nRead docs/auth.md first.' }),
    ).toBe('Goal: fix login.')
    const words = Array.from({ length: 40 }, (_, i) => `word${i}`).join(' ')
    const label = runLabel({ task: words })
    expect(label.length).toBeLessThanOrEqual(121)
    expect(label.endsWith('\u2026')).toBe(true)
    const kept = label.slice(0, -1)
    expect(words.startsWith(kept)).toBe(true)
    expect(words[kept.length]).toBe(' ')
    expect(runLabel({ title: 'Terminal title', task: words })).toBe(
      'Terminal title',
    )
    const tabbed = runLabel({ task: `${'a'.repeat(70)}\t${'b'.repeat(100)}` })
    expect(tabbed).toBe(`${'a'.repeat(70)}\u2026`)
    const astral = runLabel({ task: `${'a'.repeat(119)}\u{1F600}${'b'.repeat(50)}` })
    expect(astral).toBe(`${'a'.repeat(119)}\u{1F600}\u2026`)
  })
})

describe('run presentation', () => {
  it('keeps live stalls distinct from completed execution', () => {
    expect(runState('needs-attention')).toBe('needs-attention')
    expect(runState('completed')).toBe('done')
  })

  it('lists an unreviewed agent outcome as waiting on a human without changing its finished state', () => {
    const success = { status: 'completed', outcome_unseen: true } as const
    const failure = { status: 'failed', outcome_unseen: true } as const
    expect(runState(success.status)).toBe('done')
    expect(runState(failure.status)).toBe('failed')
    expect(waitsOnHuman(success, runState(success.status))).toBe(true)
    expect(waitsOnHuman(failure, runState(failure.status))).toBe(true)
    expect(isTerminal(success.status) && isArchivable(failure.status)).toBe(true)

    expect(waitsOnHuman({ status: 'completed' }, 'done')).toBe(false)
    expect(waitsOnHuman({ status: 'completed', outcome_unseen: false }, 'done')).toBe(false)
    // A later transition the flag outlived is not an agent outcome to review.
    expect(awaitingReview({ status: 'merged', outcome_unseen: true })).toBe(false)
    expect(awaitingReview({ status: 'running', outcome_unseen: true })).toBe(false)
  })
})
