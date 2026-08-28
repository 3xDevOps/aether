import { removeManifestItem } from '@/lib/manifest'
import type { ManifestItem } from '@/lib/types'

function item(over: Partial<ManifestItem>): ManifestItem {
  return {
    name: 'tool',
    version: '1.0.0',
    start_line: 1,
    end_line: 1,
    check_command: 'tool --version',
    ...over,
  }
}

// Line numbers: 1 FROM, 2 blank, 3-4 node (a continued RUN), 5 go
// (adjacent to node), 6 blank, 7 jq (non-adjacent).
const dockerfile = [
  'FROM ubuntu:24.04',
  '',
  'RUN apt-get update \\',
  '    && apt-get install -y nodejs=22.6.0',
  'RUN apt-get install -y golang-go=1.24.1',
  '',
  'RUN apt-get install -y jq=1.7.1',
].join('\n')
const trailing = dockerfile + '\n'

const node = () => item({ name: 'node', version: '22.6.0', start_line: 3, end_line: 4 })
const go = () => item({ name: 'go', version: '1.24.1', start_line: 5, end_line: 5 })
const jq = () => item({ name: 'jq', version: '1.7.1', start_line: 7, end_line: 7 })

describe('removeManifestItem', () => {
  it('drops the item lines and shifts adjacent and non-adjacent later spans', () => {
    const edit = removeManifestItem(trailing, [node(), go(), jq()], 'node')

    expect(edit).not.toBeNull()
    expect(edit?.dockerfile).toBe(
      [
        'FROM ubuntu:24.04',
        '',
        'RUN apt-get install -y golang-go=1.24.1',
        '',
        'RUN apt-get install -y jq=1.7.1',
      ].join('\n') + '\n',
    )
    expect(edit?.items).toEqual([
      { ...go(), start_line: 3, end_line: 3 },
      { ...jq(), start_line: 5, end_line: 5 },
    ])
  })

  it('leaves spans before the removed lines untouched', () => {
    const edit = removeManifestItem(trailing, [node(), go(), jq()], 'go')

    expect(edit?.items).toEqual([node(), { ...jq(), start_line: 6, end_line: 6 }])
    expect(edit?.dockerfile).toContain('jq=1.7.1')
    expect(edit?.dockerfile).not.toContain('golang-go')
  })

  it('does not invent a trailing newline the Dockerfile never had', () => {
    const edit = removeManifestItem(dockerfile, [node(), go(), jq()], 'go')

    expect(edit?.dockerfile).toBe(
      [
        'FROM ubuntu:24.04',
        '',
        'RUN apt-get update \\',
        '    && apt-get install -y nodejs=22.6.0',
        '',
        'RUN apt-get install -y jq=1.7.1',
      ].join('\n'),
    )
    expect(edit?.items).toEqual([node(), { ...jq(), start_line: 6, end_line: 6 }])
  })

  it('removes exactly the named item when names repeat', () => {
    const twin = item({ name: 'go', version: '1.23.0', start_line: 7, end_line: 7 })
    const edit = removeManifestItem(trailing, [node(), go(), twin], 'go')

    // Only the first match goes; the twin survives, shifted.
    expect(edit?.items).toEqual([node(), { ...twin, start_line: 6, end_line: 6 }])
  })

  it('refuses to remove the last remaining item', () => {
    const only = 'FROM ubuntu:24.04\n\nRUN apt-get install -y jq=1.7.1\n'
    const edit = removeManifestItem(only, [item({ name: 'jq', start_line: 3, end_line: 3 })], 'jq')

    expect(edit).toBeNull()
  })

  it('throws for a name that is not in the manifest', () => {
    expect(() => removeManifestItem(trailing, [node(), go()], 'rust')).toThrow(/rust/)
  })

  it('throws when the item span runs past the Dockerfile', () => {
    const short = 'FROM ubuntu:24.04\n'
    expect(() =>
      removeManifestItem(short, [node(), go()], 'go'),
    ).toThrow(/outside/)
  })
})
