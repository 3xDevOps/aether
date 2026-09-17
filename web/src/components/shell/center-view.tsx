import { useCallback, useEffect, useRef, useState } from 'react'
import type { ComponentType } from 'react'
import { lookupRoute, type RouteProps } from '@/routes'
import { useStore } from '@/store'

const terminalRouteName = 'terminal'
const maxCachedTerminals = 4
const maxInactiveCells = 4_000_000

interface CachedTerminal {
  key: string
  runID: string
  params: Record<string, string>
  View: ComponentType<RouteProps>
  weight: number
  lastUsed: number
  invalidated: boolean
}

interface CacheState {
  generation: string
  entries: CachedTerminal[]
}

function cacheGeneration(identityKey: string | null, epoch: number): string {
  return JSON.stringify([identityKey, epoch])
}

function cacheKey(generation: string, runID: string): string {
  return `${generation}\u0000${runID}`
}

function terminalWeight(cells: number): number {
  return Number.isFinite(cells) && cells > 0 ? Math.floor(cells) : 0
}

/** Keep the newest entries that fit the inactive memory budget. */
function trimCache(entries: CachedTerminal[], activeKey: string | null): CachedTerminal[] {
  const unique = [...new Map(entries.map((entry) => [entry.key, entry])).values()]
  const active = activeKey === null ? undefined : unique.find((entry) => entry.key === activeKey)
  const inactive = unique
    .filter((entry) => entry !== active)
    .sort((a, b) => b.lastUsed - a.lastUsed)
  const kept: CachedTerminal[] = []
  const limit = maxCachedTerminals - (active ? 1 : 0)
  let cells = 0

  for (const entry of inactive) {
    if (kept.length >= limit) break
    // A single recent terminal is useful even when its scrollback is larger
    // than the budget. Older entries are still discarded to keep the bound.
    if (kept.length === 0 && entry.weight > maxInactiveCells) {
      kept.push(entry)
      cells = entry.weight
      continue
    }
    if (cells + entry.weight > maxInactiveCells) continue
    kept.push(entry)
    cells += entry.weight
  }

  return active ? [active, ...kept] : kept
}

function sameEntries(a: CachedTerminal[], b: CachedTerminal[]): boolean {
  return a.length === b.length && a.every((entry, index) => entry === b[index])
}

interface TerminalRouteHostProps {
  entry: CachedTerminal
  active: boolean
  generation: string
  onWeight: (generation: string, key: string, cells: number) => void
  onInvalidate: (generation: string, key: string) => void
}

function TerminalRouteHost({
  entry,
  active,
  generation,
  onWeight,
  onInvalidate,
}: TerminalRouteHostProps) {
  const reportWeight = useCallback(
    (cells: number) => onWeight(generation, entry.key, cells),
    [entry.key, generation, onWeight],
  )
  const reportInvalidate = useCallback(
    () => onInvalidate(generation, entry.key),
    [entry.key, generation, onInvalidate],
  )
  const inactive = !active
  const View = entry.View

  return (
    <div
      className={
        inactive
          ? 'absolute inset-0 h-full w-full min-h-0 min-w-0'
          : 'relative h-full min-h-0 min-w-0'
      }
      style={inactive ? { visibility: 'hidden' } : undefined}
      inert={inactive || undefined}
      aria-hidden={inactive || undefined}
    >
      <View
        params={entry.params}
        active={active}
        onTerminalWeight={reportWeight}
        onTerminalInvalidate={reportInvalidate}
      />
    </div>
  )
}

export function CenterView() {
  const route = useStore((s) => s.route)
  const runs = useStore((s) => s.runs)
  const identityKey = useStore((s) => s.identityKey)
  const terminalCacheEpoch = useStore((s) => s.terminalCacheEpoch)
  const View = lookupRoute(route.name)
  const generation = cacheGeneration(identityKey, terminalCacheEpoch)
  const terminalRoute = route.name === terminalRouteName
  const runID = terminalRoute ? route.params.runId : undefined
  const run = runID ? runs[runID] : undefined
  const knownTerminal = terminalRoute && View !== undefined && runID !== undefined && run !== undefined
  const activeKey = knownTerminal && runID ? cacheKey(generation, runID) : null
  const [cache, setCache] = useState<CacheState>(() => ({ generation, entries: [] }))
  const recency = useRef(0)
  const pendingInvalidation = useRef(new Set<string>())
  const cacheRef = useRef(cache)
  cacheRef.current = cache
  const activeKeyRef = useRef(activeKey)
  activeKeyRef.current = activeKey

  const onWeight = useCallback((entryGeneration: string, key: string, cells: number) => {
    setCache((previous) => {
      if (previous.generation !== entryGeneration) return previous
      const entry = previous.entries.find((candidate) => candidate.key === key)
      if (!entry) return previous
      const weight = terminalWeight(cells)
      if (entry.weight === weight) return previous
      const entries = previous.entries.map((candidate) =>
        candidate.key === key ? { ...candidate, weight } : candidate,
      )
      const trimmed = trimCache(entries, activeKeyRef.current)
      return sameEntries(previous.entries, trimmed)
        ? previous
        : { ...previous, entries: trimmed }
    })
  }, [])
  const onInvalidate = useCallback((entryGeneration: string, key: string) => {
    const active = activeKeyRef.current === key
    const found =
      cacheRef.current.generation === entryGeneration &&
      cacheRef.current.entries.some((entry) => entry.key === key)
    if (!found && active) pendingInvalidation.current.add(key)
    setCache((previous) => {
      if (previous.generation !== entryGeneration) return previous
      const entries = active
        ? previous.entries.map((entry) =>
            entry.key === key && !entry.invalidated ? { ...entry, invalidated: true } : entry,
          )
        : previous.entries.filter((entry) => entry.key !== key)
      return sameEntries(previous.entries, entries)
        ? previous
        : { ...previous, entries: entries }
    })
  }, [])

  // A new authenticated owner or event-log generation must never reuse a
  // terminal view for a reused run ID.
  useEffect(() => {
    setCache((previous) => {
      if (previous.generation === generation) return previous
      pendingInvalidation.current.clear()
      return { generation, entries: [] }
    })
  }, [generation])

  // A visit makes the route current and records it in the cache. The
  // generation/key guard keeps ordinary store updates from making an entry
  // artificially newer on every render.
  const visited = useRef<{ generation: string; key: string | null }>({
    generation: '',
    key: null,
  })
  useEffect(() => {
    if (visited.current.generation === generation && visited.current.key === activeKey) return
    visited.current = { generation, key: activeKey }
    if (!knownTerminal || !View || !runID) return
    const key = activeKey as string
    const pending = pendingInvalidation.current.delete(key)
    const entry: CachedTerminal = {
      key,
      runID,
      params: route.params,
      View,
      weight: 0,
      lastUsed: ++recency.current,
      invalidated: pending,
    }
    setCache((previous) => {
      const existing = previous.generation === generation
        ? previous.entries.find((candidate) => candidate.key === key)
        : undefined
      const next = existing && !existing.invalidated && !pending
        ? { ...existing, params: route.params, View, lastUsed: entry.lastUsed }
        : entry
      const entries = previous.generation === generation
        ? [...previous.entries.filter((candidate) => candidate.key !== key), next]
        : [next]
      return {
        generation,
        entries: trimCache(entries, key),
      }
    })
  }, [activeKey, generation, knownTerminal, route.params, runID, View])

  // Run deletion and invalidation are observed independently of navigation.
  // Filtering during render below also prevents one deleted frame from being
  // displayed while this effect commits.
  useEffect(() => {
    setCache((previous) => {
      if (previous.generation !== generation) return previous
      const entries = previous.entries.filter(
        (entry) =>
          runs[entry.runID] !== undefined &&
          !(entry.invalidated && entry.key !== activeKey),
      )
      const trimmed = trimCache(entries, activeKey)
      return sameEntries(previous.entries, trimmed)
        ? previous
        : { ...previous, entries: trimmed }
    })
  }, [activeKey, generation, runs])
  const generationEntries = cache.generation === generation ? cache.entries : []
  let entries = generationEntries.filter(
    (entry) =>
      runs[entry.runID] !== undefined &&
      !(entry.invalidated && entry.key !== activeKey),
  )
  if (knownTerminal && activeKey && runID && View && !entries.some((entry) => entry.key === activeKey)) {
    entries = trimCache(
      [
        ...entries,
        {
          key: activeKey,
          runID,
          params: route.params,
          View,
          weight: 0,
          lastUsed: Number.MAX_SAFE_INTEGER,
          invalidated: false,
        },
      ],
      activeKey,
    )
  }

  return (
    <div className="relative h-full">
      {entries.map((entry) => (
        <TerminalRouteHost
          key={entry.key}
          entry={entry}
          active={entry.key === activeKey}
          generation={generation}
          onWeight={onWeight}
          onInvalidate={onInvalidate}
        />
      ))}
      {!knownTerminal && View && <View params={route.params} />}
      {!View && (
        <p className="p-4 text-sm text-muted-foreground">
          No view registered for “{route.name}”.
        </p>
      )}
    </div>
  )
}
