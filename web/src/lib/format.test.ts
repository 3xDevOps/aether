import { compactAge, elapsed, errorSentence, formatDuration, timeAgo } from '@/lib/format'

describe('errorSentence', () => {
  it('drops the RPC method and Go package names and keeps the sentence', () => {
    expect(errorSentence(new Error('run.mode.switch: scheduler: invalid run state transition: the agent has not reported its session yet')))
      .toBe('invalid run state transition: the agent has not reported its session yet')
    expect(errorSentence(new Error('/api/runs: Not Found'))).toBe('/api/runs: Not Found')
    expect(errorSentence('plain words')).toBe('plain words')
  })
})

describe('compactAge', () => {
  const now = Date.parse('2026-08-14T12:00:00Z')
  const minute = 60_000
  const hour = 60 * minute
  const day = 24 * hour
  const at = (ms: number) => new Date(now - ms).toISOString()

  it.each([
    [0, 'now', 'now'],
    [59_000, 'now', '59 seconds ago'],
    [minute, '1m', '1 minute ago'],
    [15 * minute, '15m', '15 minutes ago'],
    [59 * minute + 40_000, '1h', '1 hour ago'],
    [3 * hour, '3h', '3 hours ago'],
    [23 * hour + 40 * minute, '1d', 'yesterday'],
    [2 * day, '2d', '2 days ago'],
    [8 * day, '1w', 'last week'],
    [70 * day, '2mo', '2 months ago'],
    [800 * day, '2y', '2 years ago'],
  ])('reads %d ms ago as %s, the short form of "%s"', (ms, short, long) => {
    expect(compactAge(at(ms), now)).toBe(short)
    expect(timeAgo(at(ms), now)).toBe(long)
  })

  it('reads a time ahead of this clock as now and an unparseable one as nothing', () => {
    expect(compactAge(at(-5 * minute), now)).toBe('now')
    expect(compactAge('not a time', now)).toBe('')
  })
})

describe('formatDuration', () => {
  it.each([
    [640, '640ms'],
    [6_940, '6.9s'],
    [42_000, '42s'],
    [123_000, '2m 03s'],
    [4_320_000, '1h 12m'],
    [278_000_000, '3d 5h'],
  ])('%d ms reads %s', (ms, text) => expect(formatDuration(ms)).toBe(text))
})

describe('elapsed', () => {
  it('reads under a minute as <1m and otherwise compacts', () => {
    expect(elapsed('2026-08-14T10:00:00Z', '2026-08-14T10:00:30Z')).toBe('<1m')
    expect(elapsed('2026-08-14T10:00:00Z', Date.parse('2026-08-14T10:05:00Z'))).toBe('5m')
  })
})
