import { shellPath, shellQuote } from '@/lib/shell'

describe('shellQuote', () => {
  it.each([
    ['skills/deploy/README.md', 'skills/deploy/README.md'],
    ['a-b_c@d%e+f=g:h,i.j', 'a-b_c@d%e+f=g:h,i.j'],
    ['', "''"],
    ['skills/deploy notes/README.md', "'skills/deploy notes/README.md'"],
    ["skills/o'brien/README.md", "'skills/o'\\''brien/README.md'"],
    ['a;rm -rf b', "'a;rm -rf b'"],
    ['$HOME/x', "'$HOME/x'"],
    ['notes/*.md', "'notes/*.md'"],
  ])('quotes %j as %j', (input, want) => {
    expect(shellQuote(input)).toBe(want)
  })
})

describe('shellPath', () => {
  it.each([
    ['skills/deploy/README.md', 'skills/deploy/README.md'],
    ['-x.md', './-x.md'],
    ['--allow-secret', './--allow-secret'],
    ['-a b.md', "'./-a b.md'"],
    // Only a leading dash reads as a flag; one inside a path does not.
    ['skills/my-skill/README.md', 'skills/my-skill/README.md'],
  ])('writes %j as %j', (input, want) => {
    expect(shellPath(input)).toBe(want)
  })
})
