import { findPaths, parsePath } from '@/routes/files/paths'

const found = (text: string) => findPaths(text).map(({ start, end, ...target }) => [text.slice(start, end), target])

describe('findPaths', () => {
  it('reads a path, its line and its column', () => {
    expect(found('see web/src/app.tsx, web/src/app.tsx:42 and internal/x/y.go:12:5: undefined')).toEqual([
      ['web/src/app.tsx', { path: 'web/src/app.tsx' }],
      ['web/src/app.tsx:42', { path: 'web/src/app.tsx', line: 42 }],
      ['internal/x/y.go:12:5', { path: 'internal/x/y.go', line: 12, column: 5 }],
    ])
  })

  it('reads a path under the checkout mount, a ./ path and a #L line as repository paths', () => {
    expect(found('/workspace/web/a.ts:3 ./go.mod docs/x.md#L7')).toEqual([
      ['/workspace/web/a.ts:3', { path: 'web/a.ts', line: 3 }],
      ['./go.mod', { path: 'go.mod' }],
      ['docs/x.md#L7', { path: 'docs/x.md', line: 7 }],
    ])
  })

  it('takes a lone word only with an extension or a line', () => {
    expect(found('Run make test, then read README.md and Makefile:12.')).toEqual([
      ['README.md', { path: 'README.md' }],
      ['Makefile:12', { path: 'Makefile', line: 12 }],
    ])
  })

  it('leaves the sentence’s own punctuation and an @ mention’s sign out of the path', () => {
    expect(found('Fixed in `web/a.ts`. See (@web/b.ts).')).toEqual([
      ['web/a.ts', { path: 'web/a.ts' }],
      ['web/b.ts', { path: 'web/b.ts' }],
    ])
  })

  it('finds nothing in a URL, a path outside the checkout, a time or a number', () => {
    const text = 'https://example.com/a/b.js http://localhost:3000/src/main.ts /usr/lib/x.so ../up/x.ts ~/x/y.ts .git/config 10:42:01 3.14 1/2'
    expect(found(text)).toEqual([])
  })
})

describe('parsePath', () => {
  it('reads text that is one path and nothing else', () => {
    expect(parsePath('web/src/app.tsx:42:7')).toEqual({ path: 'web/src/app.tsx', line: 42, column: 7 })
    expect(parsePath('open web/src/app.tsx')).toBeNull()
    expect(parsePath('https://example.com/docs')).toBeNull()
  })
})
