// Palette-only equivalent of cmdk's command-score recurrence. Preserve UTF-16
// indexing, multiplication order and the original (not lowercased) query length.
const segmentBoundary = /[\\\/_+.#"@\[\(\{&]/
const wordBoundary = /[\s-]/
const boundaryScore = [0.8, 0.9, 0.17]
const categoryOrder = [2, 3, 0, 1, 4, 5] // Word, segment, interior; exact case first.

// These are constants of the scoring formula, not cached values or queries.
// Calculate each power directly: repeated multiplication changes score ties.
const gapPowers: number[] = [1]

// Synchronous scratch space only. Every cell is written before it is read in
// a later invocation; no run, query, matching list or score is reused as data.
let capacity = 0
let segments: Uint32Array
let words: Uint32Array
let boundaries: Uint8Array
let rowA: Float64Array
let rowB: Float64Array
let rowC: Float64Array
let links: Int32Array
let weighted: Float64Array
let suffix: Float64Array
let suffixWinner: Int32Array
let blockNext: Int32Array
let blockMaximum: Float64Array
let typoLinks: Int32Array
let typoSuffix: Float64Array
const heads = new Int32Array(6)
const activeCategories = new Int32Array(6)
// A partition for admissible bounds, never a cap on visited/searchable matches.
const blockSize = 16
const categorySizes = new Uint32Array(6)
// A memory budget for the optional prefix-pruning table, not an input limit.
// Larger products retain the exact three-row scorer below.
const prefixCellBudget = 1 << 20
let prefixCosts = new Uint8Array(0)
const prefixThresholds = new Float64Array(255)
let prefixWords = 0
let reachable = new Uint32Array(0)

function nextReachable(row: number, start: number): number {
  let word = start >>> 5
  if (word >= prefixWords) return -1
  let bits = reachable[row + word] & (-1 << (start & 31))
  while (bits === 0) {
    if (++word >= prefixWords) return -1
    bits = reachable[row + word]
  }
  return word * 32 + 31 - Math.clz32(bits & -bits)
}

function previousReachable(row: number, followingRow: number, start: number): number {
  if (start < 0) return -1
  let word = start >>> 5
  let bits = (reachable[row + word] | (followingRow < 0 ? 0 : reachable[followingRow + word])) &
    (-1 >>> (31 - (start & 31)))
  while (bits === 0) {
    if (--word < 0) return -1
    bits = reachable[row + word] | (followingRow < 0 ? 0 : reachable[followingRow + word])
  }
  return word * 32 + 31 - Math.clz32(bits)
}

/**
 * Find a genuine alignment, then retain every state whose optimistic prefix
 * could improve it. Interior gaps cost 2 (.17 <= 2^-2), transpositions cost 3
 * (.1 <= 2^-3); all other <=1 factors cost zero. These power-of-two bounds
 * preserve monotonicity without factoring or approximating real scores.
 * A zero result means the optional table is not in use.
 */
function preparePrefixPruning(value: string, search: string, text: string, query: string): number {
  const n = text.length
  const m = query.length
  const stride = n + 1
  const cells = (m + 1) * stride
  if (n !== value.length || m !== search.length || cells > prefixCellBudget) return 0
  // A reverse-greedy normal alignment is a real path through the recurrence.
  // Its searches cover disjoint ranges of text, and its factors are evaluated
  // in the oracle's right-to-left order. This lower bound needs no DP scratch.
  let match = text.lastIndexOf(query.charAt(m - 1))
  if (match < 0) return 0
  let lowerBound = match + 1 === n ? 1 : 0.99
  for (let q = m - 1; q >= 0; q--) {
    const previous = q === 0 || match === 0 ? -1 : text.lastIndexOf(query.charAt(q - 1), match - 1)
    if (q > 0 && previous < 0) return 0
    const start = previous + 1
    if (match !== start) {
      const boundary = boundaries[match]
      lowerBound *= boundaryScore[boundary]
      if (start > 0) {
        const gap = boundary === 0 ? segments[match - 1] - segments[start]
          : boundary === 1 ? words[match - 1] - words[start] : match - start
        lowerBound *= gapPowers[gap] ?? (gapPowers[gap] = Math.pow(0.999, gap))
      }
    }
    if (value.charAt(match) !== search.charAt(q)) lowerBound *= 0.9999
    match = previous
  }
  if (lowerBound === 0) return 0
  let maximumCost = 0
  for (let upper = 0.5; upper > lowerBound && maximumCost < 254; upper *= 0.5) maximumCost++
  if (maximumCost >= 254 || maximumCost >= 2 * m) return 0
  // Every winning path through a state must beat this continuation threshold.
  // Doubling is exact: the cost limit above keeps the positive lower bound and
  // all thresholds normal and finite. Subnormal continuations cannot beat it.
  prefixThresholds[0] = lowerBound
  for (let cost = 1; cost <= maximumCost; cost++) prefixThresholds[cost] = prefixThresholds[cost - 1] * 2

  if (prefixCosts.length < cells) {
    prefixCosts = new Uint8Array(Math.min(prefixCellBudget, Math.max(cells, prefixCosts.length * 2)))
  }
  prefixCosts.fill(255, 0, cells)
  prefixCosts[0] = 0
  prefixWords = Math.ceil(stride / 32)
  const bitCells = (m + 1) * prefixWords
  if (reachable.length < bitCells) reachable = new Uint32Array(bitCells)
  reachable.fill(0, 0, bitCells)
  reachable[0] = 1
  for (let q = 0; q < m; q++) {
    const character = query.charAt(q)
    const following = query.charAt(q + 1)
    const bitRow = q * prefixWords
    const row = q * stride
    const nextRow = row + stride
    let start = nextReachable(bitRow, 0)
    let minimum = 255
    for (let match = text.indexOf(character); match >= 0; match = text.indexOf(character, match + 1)) {
      // Include start==match: direct adjacency is considered separately, and
      // its nonadjacent bound cannot be cheaper than the adjacent transition.
      while (minimum > 0 && start >= 0 && start <= match) {
        if (prefixCosts[row + start] < minimum) minimum = prefixCosts[row + start]
        start = nextReachable(bitRow, start + 1)
      }
      const normal = Math.min(prefixCosts[row + match], minimum + (boundaries[match] === 2 ? 2 : 0))
      const destination = nextRow + match + 1
      if (normal <= maximumCost && normal < prefixCosts[destination]) {
        prefixCosts[destination] = normal
        reachable[bitRow + prefixWords + ((match + 1) >>> 5)] |= 1 << ((match + 1) & 31)
      }
      const previous = text.charAt(match - 1)
      if (q + 1 < m && minimum + 3 <= maximumCost &&
        (previous === following || (following === character && previous !== character))) {
        const transposed = destination + stride
        if (minimum + 3 < prefixCosts[transposed]) {
          prefixCosts[transposed] = minimum + 3
          reachable[bitRow + 2 * prefixWords + ((match + 1) >>> 5)] |= 1 << ((match + 1) & 31)
        }
      }
    }
  }
  return lowerBound
}

/**
 * Bottom-up DP with exact branch-and-bound, not floating-point factoring.
 * Nonadjacent matches are partitioned by boundary and case. A category's
 * maximum remaining continuation, penalized by its NEAREST remaining gap,
 * bounds every remaining candidate: both multiplication and gap penalties are
 * monotone. If that bound cannot win, skip the entire suffix, including ties.
 * Adjacent matches and transpositions are evaluated separately.
 * Small local blocks keep a distant high continuation from weakening every
 * bound before it. Their maxima use the same nearest-gap bound as a suffix.
 *
 * O(n) row scratch plus an optional O(m*n) prefix table (at most 1 MiB of
 * byte costs plus a bitset indexing its reachable states).
 * O(m*n) setup and reachable-state traversal. Candidate
 * visits are pruned but still O(m*n²) in the adversarial worst case; this is
 * not a worst-case linear-time claim. There is no recursive stack or persistent
 * search index. n and m are normalized value and query code-unit lengths.
 */
export function paletteFilter(value: string, search: string, keywords?: string[]): number {
  if (keywords?.length) value += ` ${keywords.join(' ')}`
  if (search.length === 0) return value.length === 0 ? 1 : 0.99

  const query = search.toLowerCase().replace(/[^\S ]|-/g, ' ')
  // Lowercasing cannot create ASCII symbols; separator normalization creates
  // only space. Reject absent symbols before normalizing the complete value.
  // Presence, not multiplicity, is required: swaps/duplicate typos stay valid.
  for (let q = 0; q < search.length; q++) {
    const code = query.charCodeAt(q)
    if (code < 128 && code !== 32 && (code < 97 || code > 122)) {
      const character = query.charAt(q)
      if (query.indexOf(character) === q && !value.includes(character)) return 0
    }
  }
  const text = value.toLowerCase().replace(/[^\S ]|-/g, ' ')
  if (!text.includes(query.charAt(0))) return 0

  // The first keystroke has no recursive continuation or nonzero typo path.
  // At start=0 cmdk applies no gap penalty, so no DP/prefix storage is needed.
  if (search.length === 1) {
    let best = 0
    for (let match = text.indexOf(query.charAt(0)); match >= 0; match = text.indexOf(query.charAt(0), match + 1)) {
      let score = match + 1 === value.length ? 1 : 0.99
      if (match !== 0) {
        if (segmentBoundary.test(value.charAt(match - 1))) score *= 0.8
        else if (wordBoundary.test(value.charAt(match - 1))) score *= 0.9
        else score *= 0.17
      }
      if (value.charAt(match) !== search) score *= 0.9999
      if (score > best) best = score
      // Every later candidate is nonadjacent and scores at most 0.9.
      if (best >= 0.9) break
    }
    return best
  }

  // A contiguous prefix with score >=0.9 saturates a global bound: every
  // alternative path contains a gap (at most 0.9) or typo (at most 0.1).
  // Require position-preserving normalization, and compare the normalized
  // text too: contextual lowercasing can differ even for an exact raw prefix.
  if (text.length === value.length && query.length === search.length) {
    if (text.startsWith(query)) {
      let score = text.length === query.length ? 1 : 0.99
      for (let q = query.length - 1; q >= 0; q--) {
        if (value.charAt(q) !== search.charAt(q)) score *= 0.9999
      }
      if (score >= 0.9) return score
    } else if (text.charAt(0) !== query.charAt(0)) {
      // A case-exact contiguous word match scores 0.99*0.9. With no possible
      // start-at-zero path, a second gap caps any noncontiguous path at 0.81;
      // segment/interior starts and typos score still less. Only a contiguous
      // WORD match reaching the end can beat it. Evaluate that one in the
      // oracle's right-to-left multiplication order, including case penalties.
      for (let match = value.indexOf(search); match >= 0; match = value.indexOf(search, match + 1)) {
        if (match === 0 || !wordBoundary.test(value.charAt(match - 1)) || !text.startsWith(query, match)) continue
        const partial = 0.99 * 0.9
        const end = text.length - query.length
        if (end > 0 && text.endsWith(query) && wordBoundary.test(value.charAt(end - 1))) {
          let score = 1
          for (let q = query.length - 1; q >= 0; q--) {
            if (q === 0) score *= 0.9
            if (value.charAt(end + q) !== search.charAt(q)) score *= 0.9999
          }
          return Math.max(partial, score)
        }
        return partial
      }
    }
  }

  // A swap still consumes both distinct characters; a duplicate typo only
  // repeats an existing one. Thus every distinct character before cmdk's
  // original-length base case must occur somewhere in the normalized value.
  for (let q = 1; q < search.length; q++) {
    const character = query.charAt(q)
    const code = query.charCodeAt(q)
    if (code < 128 && code !== 32 && (code < 97 || code > 122)) continue
    if (query.indexOf(character) === q && !text.includes(character)) return 0
  }

  const n = text.length
  if (capacity < n + 1) {
    capacity = Math.max(n + 1, capacity * 2)
    segments = new Uint32Array(capacity)
    words = new Uint32Array(capacity)
    boundaries = new Uint8Array(capacity)
    rowA = new Float64Array(capacity)
    rowB = new Float64Array(capacity)
    rowC = new Float64Array(capacity)
    links = new Int32Array(capacity)
    weighted = new Float64Array(capacity)
    suffix = new Float64Array(capacity)
    suffixWinner = new Int32Array(capacity)
    blockNext = new Int32Array(capacity)
    blockMaximum = new Float64Array(capacity)
    typoLinks = new Int32Array(capacity)
    typoSuffix = new Float64Array(capacity)
  }

  segments[0] = words[0] = 0
  boundaries[0] = 2
  for (let i = 0; i < n; i++) {
    const code = value.charCodeAt(i)
    let boundary = 2
    switch (code) {
      case 92: case 47: case 95: case 43: case 46: case 35:
      case 34: case 64: case 91: case 40: case 123: case 38:
        boundary = 0
        break
      default:
        if (code === 45 || code === 32 || (code >= 9 && code <= 13) ||
          (code > 127 && wordBoundary.test(value.charAt(i)))) boundary = 1
    }
    boundaries[i + 1] = boundary
    segments[i + 1] = segments[i] + Number(boundary === 0)
    words[i + 1] = words[i] + Number(boundary === 1)
  }
  const lowerBound = preparePrefixPruning(value, search, text, query)
  const pruning = lowerBound > 0

  let next = rowA
  let afterNext = rowB
  let current = rowC
  next.fill(0, 0, n + 1)
  afterNext.fill(0, 0, n + 1)
  // Lowercasing can expand a code point (İ). Keep cmdk's raw-length base case.
  for (let q = query.length; q >= 0; q--) {
    const costRow = q * (n + 1)
    const bitRow = q * prefixWords
    if (q === search.length) {
      if (pruning) {
        for (let start = nextReachable(bitRow, 0); start >= 0; start = nextReachable(bitRow, start + 1)) {
          current[start] = start === value.length ? 1 : 0.99
        }
      } else {
        current.fill(0.99, 0, n + 1)
        current[value.length] = 1
      }
    } else if (q === query.length) {
      current.fill(0, 0, n + 1)
    } else {
      const character = query.charAt(q)
      const original = search.charAt(q)
      const following = query.charAt(q + 1)
      heads.fill(-1)
      categorySizes.fill(0)
      let typoHead = -1
      let adjacentPossible = false
      // With pruning, only destinations reachable in the next one/two rows
      // can contribute a normal/typo continuation. Enumerate their bit union
      // instead of rescanning all occurrences of this character.
      const nextBitRow = bitRow + prefixWords
      const followingBitRow = q + 2 <= search.length ? nextBitRow + prefixWords : -1
      const nextCostRow = costRow + n + 1
      const afterNextCostRow = nextCostRow + n + 1
      for (let match = pruning ? previousReachable(nextBitRow, followingBitRow, n) - 1 : text.lastIndexOf(character);
        match >= 0;
        match = pruning ? previousReachable(nextBitRow, followingBitRow, match) - 1
          : match === 0 ? -1 : text.lastIndexOf(character, match - 1)) {
        if (pruning && text.charAt(match) !== character) continue
        const continuation = pruning && prefixCosts[nextCostRow + match + 1] === 255 ? 0 : next[match + 1]
        if (continuation > 0) {
          adjacentPossible = true
          const boundary = boundaries[match]
          const nonadjacent = continuation * boundaryScore[boundary]
          // Gap/case factors and every prefix are <=1. This entry cannot win
          // anywhere if even its unpenalized score cannot beat the root bound.
          // Keep its direct adjacent continuation separately, even subnormals.
          if (nonadjacent > lowerBound) {
            const category = boundary * 2 + Number(value.charAt(match) !== original)
            const head = heads[category]
            links[match] = head
            weighted[match] = nonadjacent
            if (head < 0 || nonadjacent >= suffix[head]) {
              suffix[match] = nonadjacent
              suffixWinner[match] = match
            } else {
              suffix[match] = suffix[head]
              suffixWinner[match] = suffixWinner[head]
            }
            if (categorySizes[category]++ % blockSize === 0) {
              blockNext[match] = head
              blockMaximum[match] = nonadjacent
            } else {
              blockNext[match] = blockNext[head]
              blockMaximum[match] = Math.max(nonadjacent, blockMaximum[head])
            }
            heads[category] = match
          }
        }
        const preceding = text.charAt(match - 1)
        if ((!pruning || (followingBitRow >= 0 && prefixCosts[afterNextCostRow + match + 1] !== 255)) &&
          (preceding === following || (following === character && preceding !== character))) {
          const score = afterNext[match + 1] * 0.1
          if (score > lowerBound) {
            typoLinks[match] = typoHead
            typoSuffix[match] = typoHead < 0 ? score : Math.max(score, typoSuffix[typoHead])
            typoHead = match
          }
        }
      }
      let categoryCount = 0
      for (const category of categoryOrder) {
        if (heads[category] >= 0) activeCategories[categoryCount++] = category
      }
      if (!adjacentPossible && typoHead < 0) {
        // No remaining candidate can improve the root, including adjacency.
        // Native fill avoids walking all reachable starts just to write zeros.
        if (q === 0) return lowerBound
        current.fill(0, 0, n + 1)
        const recycled = afterNext
        afterNext = next
        next = current
        current = recycled
        continue
      }

      // Without the optional prefix table, only starts following the preceding
      // one/two query characters can be read by an earlier row.
      const preceding = query.charAt(q - 1)
      const beforePreceding = query.charAt(q - 2)
      let first = !pruning && q > 0 ? text.indexOf(preceding) : -1
      let second = !pruning && q > 1 && beforePreceding !== preceding ? text.indexOf(beforePreceding) : -1
      let start = pruning ? nextReachable(bitRow, 0)
        : q === 0 ? 0 : (first < 0 ? second : second < 0 ? first : Math.min(first, second)) + 1
      while (pruning ? start >= 0 : q === 0 || first >= 0 || second >= 0) {
        // Dropping cmdk's score<0.1 guard here is exact: a transposition can
        // never exceed 0.1, so it cannot improve a candidate scoring >=0.1.
        while (typoHead >= 0 && typoHead < start) typoHead = typoLinks[typoHead]
        const threshold = pruning ? prefixThresholds[prefixCosts[costRow + start]] : 0
        let best = Math.max(threshold, typoHead < 0 ? 0 : typoSuffix[typoHead])
        if (text.charAt(start) === character && (!pruning || prefixCosts[nextCostRow + start + 1] !== 255)) {
          let adjacent = next[start + 1]
          if (value.charAt(start) !== original) adjacent *= 0.9999
          if (adjacent > best) best = adjacent
        }

        if (best < 0.9) {
          for (let c = 0; c < categoryCount; c++) {
            const category = activeCategories[c]
            let match = heads[category]
            while (match >= 0 && match <= start) match = links[match]
            heads[category] = match
            const boundary = category >> 1
            const differentCase = (category & 1) !== 0
            if (match < 0) continue
            // Seed the lower bound with an actual candidate, not a factored
            // approximation. This is the nearest maximum raw continuation,
            // which can be far beyond many weaker local blocks.
            const winner = suffixWinner[match]
            let winningScore = weighted[winner]
            if (start > 0) {
              const gap = boundary === 0 ? segments[winner - 1] - segments[start]
                : boundary === 1 ? words[winner - 1] - words[start] : winner - start
              winningScore *= gapPowers[gap] ?? (gapPowers[gap] = Math.pow(0.999, gap))
            }
            if (differentCase) winningScore *= 0.9999
            if (winningScore > best) best = winningScore
            while (match >= 0) {
              let penalty = 1
              if (start > 0) {
                const gap = boundary === 0 ? segments[match - 1] - segments[start]
                  : boundary === 1 ? words[match - 1] - words[start] : match - start
                penalty = gapPowers[gap] ?? (gapPowers[gap] = Math.pow(0.999, gap))
              }
              let upper = suffix[match] * penalty
              if (differentCase) upper *= 0.9999
              if (upper <= best) break
              let localUpper = blockMaximum[match] * penalty
              if (differentCase) localUpper *= 0.9999
              if (localUpper <= best) {
                match = blockNext[match]
                continue
              }
              let score = weighted[match] * penalty
              if (differentCase) score *= 0.9999
              if (score > best) best = score
              match = links[match]
            }
          }
        }
        // The synthetic threshold is never a continuation. A real suffix at
        // or below it cannot beat the retained genuine root lowerBound, even
        // with its most optimistic prefix. Equal scores need no new witness.
        current[start] = best > threshold ? best : 0
        if (q === 0) return Math.max(lowerBound, best)
        if (pruning) start = nextReachable(bitRow, start + 1)
        else {
          if (first === start - 1) first = text.indexOf(preceding, first + 1)
          if (second === start - 1) second = text.indexOf(beforePreceding, second + 1)
          start = (first < 0 ? second : second < 0 ? first : Math.min(first, second)) + 1
        }
      }
    }
    const recycled = afterNext
    afterNext = next
    next = current
    current = recycled
  }
  return lowerBound
}
