import { useState } from 'react'
import { Callout } from '@/components/ui/callout'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { Api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import { Charts } from '@/routes/insights/system/charts'
import { Consumers } from '@/routes/insights/system/consumers'
import { Kpis } from '@/routes/insights/system/kpis'
import { inRange, ranges, type RangeID } from '@/routes/insights/system/model'
import { StorageSection } from '@/routes/insights/system/storage'
import { useSystemStats, type SystemStatsState } from '@/routes/insights/system/use-system-stats'

function RangePicker({ range, onChange }: { range: RangeID; onChange: (r: RangeID) => void }) {
  return (
    <Tabs value={range} onValueChange={(value) => onChange(value as RangeID)}>
      <TabsList look="segmented" aria-label="Time range">
        {ranges.map((r) => <TabsTrigger key={r.id} value={r.id}>{r.id}</TabsTrigger>)}
      </TabsList>
    </Tabs>
  )
}

function liveLine({ error, paused, intervalMs }: SystemStatsState): string {
  if (error) return error
  if (paused === 'hidden') return 'Paused while this tab is hidden'
  if (paused === 'offline') return 'Paused while the server is unreachable'
  return `Live · every ${intervalMs / 1000}s`
}

export function SystemTab({ client }: { client: Api }) {
  const connection = useStore((s) => s.connection)
  const disk = useStore((s) => s.info?.disk)
  const state = useSystemStats(client, connection)
  const [range, setRange] = useState<RangeID>('15m')
  const { stats } = state
  const rangeMs = ranges.find((r) => r.id === range)!.ms

  return (
    <div className="flex min-w-0 flex-col gap-8">
      {state.unsupported ? (
        <Callout tone="neutral" role="status">This server does not report system stats. Update the server to see them.</Callout>
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <RangePicker range={range} onChange={setRange} />
            <p role="status" className={cn('min-w-0 whitespace-pre-wrap break-words text-ui-sm', state.error ? 'text-state-failed' : 'text-muted')}>{liveLine(state)}</p>
          </div>
          {!stats && !state.error && <p className="text-ui text-muted">Reading system stats…</p>}
          {stats && (
            <div className={cn('flex min-w-0 flex-col gap-8', state.error && 'opacity-60')}>
              {stats.hostError && <Callout tone="failed" role="alert" className="whitespace-pre-wrap">{stats.hostError}</Callout>}
              <Kpis stats={stats} samples={inRange(stats.samples, rangeMs)} disk={disk} />
              <Consumers stats={stats} />
              <Charts samples={inRange(stats.samples, rangeMs)} cores={stats.host.cpu_cores} memoryTotal={stats.host.memory_total_bytes} rangeMs={rangeMs} />
            </div>
          )}
        </>
      )}
      <StorageSection />
    </div>
  )
}
