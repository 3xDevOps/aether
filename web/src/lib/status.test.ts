import { runLabel, runState } from '@/lib/status'

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

  it('keeps unanswered work in Needs you after a terminal lifecycle', () => {
    for (const status of ['failed', 'completed', 'merged', 'abandoned'] as const) {
      expect(runState(status, true)).toBe('needs-attention')
    }
  })
})
