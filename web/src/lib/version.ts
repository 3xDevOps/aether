// Semver precedence as internal/selfupdate/check.go orders it: prerelease
// identifiers compare numerically when both are numbers, and a `git
// describe` tail ranks a source build just above the tag it descends from.

interface Parsed {
  nums: [number, number, number]
  pre: string[]
  ahead: boolean
}

const describeTail = /-[0-9]+-g[0-9a-f]{4,}(-dirty)?$/

function parse(tag: string): Parsed | null {
  let rest = tag.replace(/^v/, '')
  const plus = rest.indexOf('+')
  if (plus >= 0) rest = rest.slice(0, plus)
  let ahead = false
  const tail = describeTail.exec(rest)
  if (tail) {
    rest = rest.slice(0, tail.index)
    ahead = true
  } else if (rest.endsWith('-dirty')) {
    rest = rest.slice(0, -'-dirty'.length)
    ahead = true
  }
  const dash = rest.indexOf('-')
  const core = dash >= 0 ? rest.slice(0, dash) : rest
  const pre = dash >= 0 ? rest.slice(dash + 1).split('.') : []
  const fields = core.split('.')
  if (fields.length !== 3 || !fields.every((f) => /^[0-9]+$/.test(f))) return null
  if (pre.some((id) => id === '')) return null
  return { nums: fields.map(Number) as [number, number, number], pre, ahead }
}

function sign(a: number, b: number): number {
  return a < b ? -1 : a > b ? 1 : 0
}

function comparePre(a: string, b: string): number {
  const an = /^[0-9]+$/.test(a)
  const bn = /^[0-9]+$/.test(b)
  if (an && bn) return sign(Number(a), Number(b))
  if (an) return -1
  if (bn) return 1
  return a === b ? 0 : a < b ? -1 : 1
}

/** Negative, zero or positive; null when either side is not a release
 * version (`dev`, `(devel)`, a bare commit). */
export function compareVersions(a: string, b: string): number | null {
  const x = parse(a)
  const y = parse(b)
  if (!x || !y) return null
  for (let i = 0; i < 3; i++) {
    if (x.nums[i] !== y.nums[i]) return sign(x.nums[i], y.nums[i])
  }
  if (x.pre.length === 0 || y.pre.length === 0) {
    if (x.pre.length !== y.pre.length) return x.pre.length === 0 ? 1 : -1
  } else {
    for (let i = 0; i < x.pre.length && i < y.pre.length; i++) {
      const c = comparePre(x.pre[i], y.pre[i])
      if (c !== 0) return c
    }
    const c = sign(x.pre.length, y.pre.length)
    if (c !== 0) return c
  }
  return x.ahead === y.ahead ? 0 : x.ahead ? 1 : -1
}

/** selfupdate.Behind: an unparseable version is never behind. */
export function behind(running: string, latest: string): boolean {
  return (compareVersions(running, latest) ?? 0) < 0
}
