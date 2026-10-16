import { describe, expect, it } from 'vitest'
import { behind, compareVersions } from '@/lib/version'

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

describe('behind', () => {
  it('orders numeric prereleases numerically', () => {
    expect(behind('v0.7.0-alpha.9', 'v0.7.0-alpha.11')).toBe(true)
    expect(behind('v0.7.0-alpha.11', 'v0.7.0-alpha.9')).toBe(false)
  })

  it('never calls an unparseable version behind, in either direction', () => {
    expect(behind('dev', 'v9.9.9')).toBe(false)
    expect(behind('v0.1.0', 'dev')).toBe(false)
  })

  it('counts a describe tail of tag T as at least T', () => {
    expect(behind('v0.8.0-3-g1234abcd', 'v0.8.0')).toBe(false)
  })
})
