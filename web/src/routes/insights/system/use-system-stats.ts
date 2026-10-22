import { useEffect, useRef, useState } from 'react'
import { ApiError, type Api } from '@/lib/api'
import { useStore } from '@/store'
import { message } from '@/lib/format'
import type { SystemContainer, SystemHost, SystemOthers, SystemSample, SystemStatsResult } from '@/lib/types'
import { appendHistory, mergeSamples, type History } from '@/routes/insights/system/model'

const METHOD_NOT_FOUND = -32601
const defaultIntervalMs = 5000
const measuredAgainMs = 1500
const answerWithinMs = 15_000

export interface SystemStats {
  host: SystemHost
  hostError: string
  samples: SystemSample[]
  containers: SystemContainer[]
  containersAt: number
  containersError: string
  others?: SystemOthers
  history: History
}

export interface SystemStatsState {
  stats: SystemStats | null
  /** The last poll's error text; the previous data stays in `stats`. */
  error: string
  unsupported: boolean
  paused: 'hidden' | 'offline' | null
  intervalMs: number
}

function fold(previous: SystemStats | null, result: SystemStatsResult): SystemStats {
  const at = result.containers_at_ms ?? 0
  const sameSnapshot = previous !== null && previous.containersAt === at
  return {
    host: result.host,
    hostError: result.host_error ?? '',
    samples: mergeSamples(previous?.samples ?? [], result.samples, result.retention_ms),
    containers: result.containers,
    containersAt: at,
    containersError: result.containers_error ?? '',
    others: result.others,
    history: at === 0 || sameSnapshot ? previous?.history ?? new Map() : appendHistory(previous?.history ?? new Map(), result.containers, at),
  }
}

function visible(): boolean {
  return document.visibilityState === 'visible'
}

export function useSystemStats(client: Api, connection: string): SystemStatsState {
  const [stats, setStats] = useState<SystemStats | null>(null)
  const [error, setError] = useState('')
  const [unsupported, setUnsupported] = useState(false)
  const [intervalMs, setIntervalMs] = useState(defaultIntervalMs)
  const [shown, setShown] = useState(visible)
  const newest = useRef(0)
  const every = useRef(defaultIntervalMs)
  const identity = useStore((s) => s.identityKey)

  useEffect(() => {
    setStats(null)
    setError('')
    setUnsupported(false)
    newest.current = 0
  }, [identity])

  useEffect(() => {
    const onChange = () => setShown(visible())
    document.addEventListener('visibilitychange', onChange)
    return () => document.removeEventListener('visibilitychange', onChange)
  }, [])

  const polling = connection === 'live' && shown && !unsupported

  useEffect(() => {
    if (!polling) return
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let inFlight: AbortController | undefined
    const poll = async (again: boolean) => {
      let wait = every.current
      const controller = new AbortController()
      inFlight = controller
      const deadline = setTimeout(() => controller.abort(new Error(`system.stats did not answer within ${answerWithinMs / 1000}s`)), answerWithinMs)
      try {
        const result = await client.systemStats({ since_ms: newest.current }, controller.signal)
        if (stopped) return
        newest.current = Math.max(newest.current, result.samples.at(-1)?.t_ms ?? 0)
        setStats((previous) => fold(previous, result))
        setError('')
        every.current = result.interval_ms || defaultIntervalMs
        setIntervalMs(every.current)
        wait = every.current
        if (!again && result.containers.some((c) => c.cpu_cores === undefined)) {
          timer = setTimeout(() => void poll(true), measuredAgainMs)
          return
        }
      } catch (e) {
        if (stopped) return
        if (e instanceof ApiError && (e.status === 404 || e.code === METHOD_NOT_FOUND)) {
          setUnsupported(true)
          return
        }
        setError(message(e))
      } finally {
        clearTimeout(deadline)
      }
      timer = setTimeout(() => void poll(false), wait)
    }
    void poll(false)
    return () => {
      stopped = true
      clearTimeout(timer)
      inFlight?.abort()
    }
  }, [client, polling, identity])

  return { stats, error, unsupported, paused: !shown ? 'hidden' : connection === 'live' ? null : 'offline', intervalMs }
}
