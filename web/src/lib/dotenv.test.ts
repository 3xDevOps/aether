import { parseDotenv } from '@/lib/dotenv'

// cmd/aether/workspace_env_test.go holds the same cases for the CLI's parser.
describe('parseDotenv', () => {
  it('reads NAME=VALUE lines and numbers the ones that are not', () => {
    const text = [
      '# a comment',
      '',
      'PLAIN=value',
      'export EXPORTED=yes',
      '  SPACED = padded value  ',
      'DOUBLE="two\\nlines and a \\"quote\\""',
      "SINGLE='kept $as \\n written'",
      'COMMENTED=value # trailing comment',
      'QUOTED_COMMENT="abc # kept" # dropped',
      "SINGLE_COMMENT='x'#y",
      'OPEN_QUOTE="never closed',
      'HASH=abc#def',
      'URL=https://example.test/?a=b',
      'EMPTY=',
      'PLAIN=last wins',
      'no equals sign',
      '=no name',
      'TWO WORDS=x',
    ].join('\r\n')

    expect(parseDotenv(text)).toEqual({
      variables: [
        { name: 'PLAIN', value: 'last wins' },
        { name: 'EXPORTED', value: 'yes' },
        { name: 'SPACED', value: 'padded value' },
        { name: 'DOUBLE', value: 'two\nlines and a "quote"' },
        { name: 'SINGLE', value: 'kept $as \\n written' },
        { name: 'COMMENTED', value: 'value' },
        { name: 'QUOTED_COMMENT', value: 'abc # kept' },
        { name: 'SINGLE_COMMENT', value: 'x' },
        { name: 'OPEN_QUOTE', value: '"never closed' },
        { name: 'HASH', value: 'abc#def' },
        { name: 'URL', value: 'https://example.test/?a=b' },
        { name: 'EMPTY', value: '' },
      ],
      badLines: [16, 17, 18],
    })
  })
})
