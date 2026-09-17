import { deletesInLabel } from '@/lib/format'

describe('deletesInLabel', () => {
  const now = new Date('2026-08-20T10:00:00Z').getTime()

  it('reads "today" for a date already past due', () => {
    expect(deletesInLabel('2026-08-20T08:00:00Z', now)).toBe('deleted today')
  })

  it('reads "today" two hours out', () => {
    expect(deletesInLabel('2026-08-20T12:00:00Z', now)).toBe('deleted today')
  })

  it('reads "in 1 day" at 24h01m, not "in 2 days"', () => {
    expect(deletesInLabel('2026-08-21T10:01:00Z', now)).toBe('deleted in 1 day')
  })

  it('reads "in 2 days" once 48h have passed', () => {
    expect(deletesInLabel('2026-08-22T10:00:00Z', now)).toBe('deleted in 2 days')
  })

  it('reads "in N days" further out', () => {
    expect(deletesInLabel('2026-08-28T10:00:00Z', now)).toBe('deleted in 8 days')
  })

  it('returns an empty label for an unparseable date, so no badge renders', () => {
    expect(deletesInLabel('not-a-date', now)).toBe('')
  })
})
