import type { PatchLine } from '@/routes/diff/parse'
import { lineLabel, locate, placeComments, reach, reviewEntries, reviewMessage, type ReviewComment } from '@/routes/diff/review'

const add = (text: string, line: number): PatchLine => ({ kind: 'add', text, new: line })
const del = (text: string, line: number): PatchLine => ({ kind: 'del', text, old: line })
const hunk: PatchLine = { kind: 'hunk', text: '@@ -1,2 +1,3 @@' }

function comment(lines: PatchLine[], over: Partial<ReviewComment> = {}): ReviewComment {
  return { id: 'c1', path: 'a.go', scope: '', lines, body: 'why', ...over }
}

describe('locate', () => {
  const brace = add('}', 4)

  it('prefers the block still at its line numbers over an identical one elsewhere', () => {
    expect(locate([hunk, add('}', 2), add('x', 3), brace], [brace])).toBe(3)
  })

  it('follows a block that moved only when nothing else matches it', () => {
    expect(locate([hunk, add('y', 2), add('x', 9)], [add('x', 3)])).toBe(2)
    expect(locate([hunk, add('}', 7), add('}', 9)], [brace])).toBe(-1)
  })

  it('does not match a line that changed side', () => {
    expect(locate([hunk, del('x', 3)], [add('x', 3)])).toBe(-1)
  })
})

test('a range stops at the edge of its hunk', () => {
  const lines = [hunk, add('a', 1), add('b', 2), hunk, add('c', 9)]
  expect(reach(lines, 1, 4)).toBe(2)
  expect(reach(lines, 4, 1)).toBe(4)
  expect(reach(lines, 2, 1)).toBe(1)
})

test('a comment from another diff shows where its lines are and is not called outdated where they are not', () => {
  const here = comment([add('x', 3)])
  const elsewhere = comment([add('gone', 5)], { id: 'c2', scope: 'aaa..bbb' })
  const missing = comment([add('gone', 5)], { id: 'c3' })
  const placed = placeComments([hunk, add('x', 3)], [here, elsewhere, missing], '')
  expect(placed.placed).toEqual([{ comment: here, start: 1, end: 1 }])
  expect(placed.outdated).toEqual([missing])
})

test('lines are named by the new file unless every one was removed', () => {
  expect(lineLabel([del('a', 4), add('b', 4), add('c', 5)])).toBe('4-5')
  expect(lineLabel([add('b', 4)])).toBe('4')
  expect(lineLabel([del('a', 4), del('b', 5)])).toBe('4-5 (removed)')
})

test('the message orders comments by file then line and fences a quote that holds a fence', () => {
  const files = [
    { path: 'a.go', status: 'modified' as const, additions: 2, deletions: 1, lines: [hunk, del('old', 1), add('```', 1), add('new', 2)] },
    { path: 'b.go', status: 'deleted' as const, additions: 0, deletions: 1, lines: [hunk, del('gone', 7)] },
  ]
  const entries = reviewEntries([
    comment([del('gone', 7)], { id: 'c1', path: 'b.go', body: 'keep this' }),
    comment([add('new', 2)], { id: 'c2', body: 'second' }),
    comment([del('old', 1), add('```', 1)], { id: 'c3', body: '', draft: ' first ' }),
    comment([add('x', 1)], { id: 'c4', body: '' }),
  ], files, '')

  expect(reviewMessage(entries)).toBe([
    "3 review comments on your changes. Each quotes the diff lines it is about; line numbers are the new file's unless marked removed.",
    '',
    '1. a.go:1',
    '````diff',
    '-old',
    '+```',
    '````',
    'first',
    '',
    '2. a.go:2',
    '```diff',
    '+new',
    '```',
    'second',
    '',
    '3. b.go:7 (removed)',
    '```diff',
    '-gone',
    '```',
    'keep this',
  ].join('\n'))
})
