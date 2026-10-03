import { defaultFilter } from 'cmdk'
import { paletteFilter } from '@/lib/palette-filter'

const candidates = [
  'profile', 'Profile', 'profile service', 'user profile', 'user/profile',
  'user_profile', 'user.profile', 'unprofiled', 'pro file', 'prfoile',
  'project file', 'profile 0001', 'profile 0100', 'profile 1000',
  'remote\tprofile', 'remote-profile', 'archive run', 'restore run',
  'rewrite checkout flow branch/checkout claude Payments run_42',
]

function ranking(filter: typeof defaultFilter, values: string[], query: string) {
  return values
    .map((value) => ({ value, score: filter(value, query) }))
    .filter(({ score }) => score > 0)
    .sort((a, b) => b.score - a.score)
    .map(({ value }) => value)
}

describe('paletteFilter', () => {
  it.each(['profile', 'prf', 'profiel', 'proffile', 'profile 0001', 'restore', 'p', '', 'zzzz'])(
    'preserves membership, ranking and exact tie scores for %j',
    (query) => {
      expect(ranking(paletteFilter, candidates, query)).toEqual(ranking(defaultFilter, candidates, query))
      for (const value of candidates) {
        expect(paletteFilter(value, query), `${value} / ${query}`).toBe(defaultFilter(value, query))
      }
    },
  )

  it('keeps exact-case, word-boundary, segment-boundary and interior matches ordered', () => {
    const values = ['unprofiled', 'user/profile', 'user profile', 'Profile', 'profile']
    const expected = ['profile', 'Profile', 'user profile', 'user/profile', 'unprofiled']
    expect(ranking(defaultFilter, values, 'profile')).toEqual(expected)
    expect(ranking(paletteFilter, values, 'profile')).toEqual(expected)
  })

  it('preserves input order on score ties rather than introducing a lexical tiebreak', () => {
    const values = ['profile z', 'profile a', 'profile m']
    expect(ranking(defaultFilter, values, 'profile')).toEqual(values)
    expect(ranking(paletteFilter, values, 'profile')).toEqual(values)
    expect(paletteFilter(values[0], 'profile')).toBe(paletteFilter(values[1], 'profile'))
  })

  it.each(['profiel', 'proffile'])(
    'retains the oracle’s typo reachability for %s',
    (query) => {
      expect(defaultFilter('profile', query)).toBeGreaterThan(0)
      expect(paletteFilter('profile', query)).toBe(defaultFilter('profile', query))
      expect(ranking(paletteFilter, candidates, query)).toEqual(ranking(defaultFilter, candidates, query))
    },
  )

  it.each([
    ['Éclair café', 'éc'],
    ['Ångström résumé', 'år'],
    ['東京の修正', '東修'],
    ['deploy 𠮷 agent', '𠮷'],
    ['İstanbul', 'i'],
    ['İstanbul', 'İ'],
    ['foo\u00a0bar\nquux', 'foo bar'],
    ['a\\b/c_d+e.f#g"h@i[j(k{l&m', 'acgim'],
    ['a-a\ta\na a-a', 'aaa'],
  ])('matches UTF-16 and boundary behavior for %j / %j', (value, query) => {
    const expected = defaultFilter(value, query)
    expect(expected).toBeGreaterThan(0)
    expect(paletteFilter(value, query)).toBe(expected)
  })

  it('searches keywords in the same order and with the same separators as cmdk', () => {
    const value = 'Hand off to teammate'
    const keywords = ['Bob', 'member_42', 'Payments']
    for (const query of ['Bob', 'member_42', 'Payments', 'teammate Bob', 'b42']) {
      const expected = defaultFilter(value, query, keywords)
      expect(expected).toBeGreaterThan(0)
      expect(paletteFilter(value, query, keywords)).toBe(expected)
    }
    expect(paletteFilter(value, 'Payments')).toBe(0)
  })

  it('keeps task tails and every run search field reachable without a prefix cap', () => {
    const task = `${'Review the implementation and retain the existing behavior. '.repeat(100)}zyzzyva`
    const values = [
      `${task} feature/checkouts claude Payments run_42`,
      'short unrelated task feature/accounts codex Accounts run_43',
    ]
    for (const query of ['zyzzyva', 'checkouts', 'claude', 'Payments', 'run_42']) {
      expect(defaultFilter(values[0], query)).toBeGreaterThan(0)
      expect(paletteFilter(values[0], query)).toBe(defaultFilter(values[0], query))
      expect(ranking(paletteFilter, values, query)).toEqual(ranking(defaultFilter, values, query))
    }
  })

  it('keeps exact scores and ties when distant suffix maxima have different gap penalties', () => {
    const gaps = ['x'.repeat(512), ' /-'.repeat(32), '\t-'.repeat(32)]
    const values = [
      'profile', 'Profile', 'user/profile', 'user profile',
      ...gaps.flatMap((gap) => [
        `p${gap}rofile z`,
        `p${gap}rofile a`,
        `pr${gap}ofile`,
        `prof${gap}ile`,
        `p${gap}ROFILE`,
        `p${gap}profile profile`,
      ]),
    ]
    for (const query of ['profile', 'prf', 'profiel', 'proffile']) {
      expect(ranking(paletteFilter, values, query)).toEqual(ranking(defaultFilter, values, query))
      for (const value of values) {
        expect(paletteFilter(value, query), `${JSON.stringify(value)} / ${query}`).toBe(defaultFilter(value, query))
      }
    }
    for (const gap of gaps) {
      expect(paletteFilter(`p${gap}rofile z`, 'profile')).toBe(paletteFilter(`p${gap}rofile a`, 'profile'))
    }
  })

  it('ranks terminal contiguous matches against earlier partial, case-mismatched and prefix paths', () => {
    const values = [
      'x profile later',
      'x profile x profile',
      'x profile x PROFILE',
      'x profile x pRoFiLe',
      'x profile xprofile',
      'profile x profile',
      'PROFILE x profile',
      'pro profile file',
      'p profile rofile',
      'x profile profile z',
      'x profile profile a',
    ]
    expect(defaultFilter('x profile x profile', 'profile')).toBeGreaterThan(defaultFilter('x profile later', 'profile'))
    expect(defaultFilter('pro profile file', 'profile')).toBeGreaterThan(defaultFilter('x profile later', 'profile'))
    for (const query of ['profile', 'PROFILE', 'pRoFiLe', 'pro', 'profiel', 'proffile']) {
      expect(ranking(paletteFilter, values, query)).toEqual(ranking(defaultFilter, values, query))
      for (const value of values) {
        expect(paletteFilter(value, query), `${value} / ${query}`).toBe(defaultFilter(value, query))
      }
    }
  })

  it.each([
    ['x ΟΣΑ', 'ΟΣ'],
    ['ΟΣΑ ΟΣ', 'ΟΣ'],
    ['x ΟΣ x ΟΣ', 'ΟΣ'],
    ['x İstanbul İstanbul', 'İstanbul'],
    ['x 𠮷 x 𠮷', '𠮷'],
    ['x foo-bar foo bar', 'foo bar'],
    ['FOO BAR x foo-bar', 'foo-bar'],
    ['x aaaa x AAAA', 'aaaa'],
    ['x aa aa', 'aaa'],
    ['ba', 'ab'],
    ['a', 'aa'],
    ['ab', 'acb'],
  ])('preserves normalization, duplicate and swap behavior for %j / %j', (value, query) => {
    expect(paletteFilter(value, query)).toBe(defaultFilter(value, query))
  })

  it('preserves symbols beside case expansions, normalized separators and symbol typos', () => {
    for (const [value, query] of [
      ['K_1', 'k_1'],
      ['İ_1', 'i\u0307_1'],
      ['İ', 'İ1'],
      ['row\t1', 'row-1'],
      ['row-1', 'row 1'],
      ['task?!_1', 'task!?_1'],
      ['id_1', 'id_11'],
    ]) {
      const expected = defaultFilter(value, query)
      expect(expected).toBeGreaterThan(0)
      expect(paletteFilter(value, query), `${value} / ${query}`).toBe(expected)
    }
    expect(defaultFilter('x_１', 'x_1')).toBe(0)
    expect(paletteFilter('x_１', 'x_1')).toBe(0)
  })

  it('preserves distant winners and stable ties after long plateaus of weaker continuations', () => {
    const plateau = `preamble ${'profile pro file / PROFILE - '.repeat(37)}`
    const values = [
      `${plateau}workspace z run_0001`,
      `${plateau}workspace a run_0001`,
      `${plateau}profile workspace z run_0001`,
      `${plateau}PROFILE workspace z run_0001`,
      `${plateau}profile workspace z run_0010`,
      `${plateau}profile workspace z run_1000`,
      `${plateau}pro file workspace z run_0001`,
      `${plateau}profile 0001 trailing`,
    ]
    for (const query of ['profile 0001', 'Profile 0001', 'profile 001', 'profiel 0001', 'proffile 0001']) {
      expect(ranking(paletteFilter, values, query)).toEqual(ranking(defaultFilter, values, query))
      for (const value of values) {
        expect(paletteFilter(value, query), `${value} / ${query}`).toBe(defaultFilter(value, query))
      }
    }
    expect(paletteFilter(values[0], 'profile 0001')).toBeGreaterThan(0)
    expect(paletteFilter(values[0], 'profile 0001')).toBe(paletteFilter(values[1], 'profile 0001'))
  })

  it('retains a better typo path when a genuine normal alignment has a long interior gap', () => {
    const swapped = `a${'x'.repeat(800)}ba`
    const repeated = `a${'x'.repeat(800)}a`
    expect(defaultFilter(swapped, 'ab')).toBe(0.1)
    expect(defaultFilter(repeated, 'aa')).toBe(0.1)
    expect(paletteFilter(swapped, 'ab')).toBe(0.1)
    expect(paletteFilter(repeated, 'aa')).toBe(0.1)
    for (const query of ['ab', 'aa', 'aab', 'aba', 'abb']) {
      const values = [swapped, repeated, `a${' /-'.repeat(100)}ba`, 'a/b', 'ba', 'cba']
      expect(ranking(paletteFilter, values, query)).toEqual(ranking(defaultFilter, values, query))
      for (const value of values) {
        expect(paletteFilter(value, query)).toBe(defaultFilter(value, query))
      }
    }
  })

  it('preserves typo-only matches and ranking when no complete normal subsequence exists', () => {
    const prose = 'preamble profile project file / Profile - '.repeat(19)
    const values = [
      `${prose}workspace run_0012`,
      `${prose}workspace run_0102`,
      `${prose}workspace run_1002`,
      `${prose}workspace run_0012 trailing`,
      `${prose}PROFILE workspace run_0012`,
      `${prose}pro file workspace run_0012`,
      'profile 0012',
      'profile 0123',
    ]
    expect(defaultFilter(values[0], 'profile 0001')).toBeGreaterThan(0)
    for (const query of ['profile 0001', 'Profile 0001', 'profiel 0001', 'proffile 0001']) {
      expect(ranking(paletteFilter, values, query)).toEqual(ranking(defaultFilter, values, query))
      for (const value of values) {
        expect(paletteFilter(value, query), `${value} / ${query}`).toBe(defaultFilter(value, query))
      }
    }
  })

  it('keeps very long task tails reachable and subsequent short searches independent', () => {
    const value = `${'x'.repeat(360_000)} a/b`
    const expected = defaultFilter(value, 'ab')
    expect(expected).toBeGreaterThan(0)
    expect(paletteFilter(value, 'ab')).toBe(expected)
    for (const [shortValue, query] of [['preamble a/b', 'ab'], ['ba', 'ab'], ['İstanbul', 'İ'], ['cab', 'ba']]) {
      expect(paletteFilter(shortValue, query)).toBe(defaultFilter(shortValue, query))
    }
  })

  it('retains positive subnormal matches at the underflow boundary of an enormous task', () => {
    for (const gap of [744_000, 742_000]) {
      const value = `a${'x'.repeat(gap)}b`
      const expected = defaultFilter(value, 'ab')
      expect(expected).toBe(gap === 742_000 ? Number.MIN_VALUE : 0)
      expect(paletteFilter(value, 'ab')).toBe(expected)
    }
  })

  it('retains transposition winners and their case-insensitive ties beside stronger normal matches', () => {
    const values = ['ba', 'bA', 'ab']
    expect(defaultFilter('ba', 'ab')).toBe(0.1)
    expect(ranking(paletteFilter, values, 'ab')).toEqual(['ab', 'ba', 'bA'])
    for (const query of ['ab', 'aa', 'aab', 'aba', 'abb']) {
      for (const value of [...values, 'b b a', 'b/b/a', 'aab', 'ababab', 'aaAAaa']) {
        expect(paletteFilter(value, query), `${value} / ${query}`).toBe(defaultFilter(value, query))
      }
    }
  })

  it('does not let earlier long searches affect later short, Unicode, missing or keyword searches', () => {
    const searches: [string, string, string[]?][] = [
      [`${'profile / Profile - '.repeat(24)}0001`, 'profile 0001'],
      ['ba', 'ab'],
      ['İstanbul', 'İ'],
      ['foo\u00a0bar', 'fb'],
      ['profile', 'profiel'],
      ['Hand off', 'Bob', ['Bob', 'member_42']],
      ['missing', 'zz'],
      ['profile', 'p'],
      ['PROFILE', 'p'],
      ['a', 'aa'],
      ['ab', 'ab'],
      ['', 'a'],
    ]
    for (const sequence of [searches, [...searches].reverse(), searches]) {
      for (const [value, query, keywords] of sequence) {
        expect(paletteFilter(value, query, keywords), `${value} / ${query}`).toBe(defaultFilter(value, query, keywords))
      }
    }
  })

  it('agrees on repeated-character and overlapping typo paths, including zero-score exclusions', () => {
    const alphabet = ['a', 'b', 'A', ' ', '/', '-']
    let values = ['']
    for (let length = 0, layer = ['']; length < 4; length++) {
      layer = layer.flatMap((prefix) => alphabet.map((letter) => prefix + letter))
      values = values.concat(layer)
    }
    for (const query of ['a', 'b', 'aa', 'ab', 'ba', 'aab', 'aba', 'abb', 'a b', 'A', 'zz']) {
      for (const value of values) {
        expect(paletteFilter(value, query), `${JSON.stringify(value)} / ${query}`).toBe(defaultFilter(value, query))
      }
    }
  })
})
