import { describe, expect, it } from 'vitest'
import { compareVersions } from '@/lib/version'

describe('compareVersions', () => {
  it.each([
    ['v0.8.0', 'v0.8.0', 0],
    ['0.8.0', 'v0.8.0', 0],
    ['v0.7.9', 'v0.8.0', -1],
    ['v0.10.0', 'v0.9.0', 1],
    ['v0.8.0-alpha.2', 'v0.8.0-alpha.10', -1],
    ['v0.8.0-alpha.10', 'v0.8.0-beta.1', -1],
    ['v0.8.0-alpha', 'v0.8.0-alpha.1', -1],
    ['v0.8.0-alpha.1', 'v0.8.0-alpha.x', -1],
    ['v0.8.0-rc.1', 'v0.8.0', -1],
    ['v0.8.0-alpha.3-5-gabc1234', 'v0.8.0-alpha.3', 1],
    ['v0.8.0-alpha.3-5-gabc1234-dirty', 'v0.8.0-alpha.4', -1],
    ['v0.8.0-dirty', 'v0.8.0', 1],
    ['v0.8.0+build.7', 'v0.8.0', 0],
  ])('%s vs %s', (a, b, want) => {
    expect(compareVersions(a, b)).toBe(want)
  })

  it.each(['dev', '(devel)', 'abc1234', 'v1.2', 'v1.2.3-', 'v1.2.3-a..b'])('%s is not a release', (v) => {
    expect(compareVersions(v, 'v0.8.0')).toBeNull()
  })
})
