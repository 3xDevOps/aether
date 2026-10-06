import { TriangleAlert } from '@/components/icons'
import { registerSlot, type CardSlotProps } from '@/components/slots'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import type { MissionScopeDiagnostic } from '@/lib/types'
import { useStore } from '@/store'

const noDiagnostics: MissionScopeDiagnostic[] = []

function detailOf(diagnostic: MissionScopeDiagnostic): string {
  if (diagnostic.unavailable) return diagnostic.unavailable_why || diagnostic.detail || 'snapshot or evidence is unavailable'
  return diagnostic.detail || diagnostic.paths.join('\n') || 'No paths reported'
}

function SwarmConflicts({ run }: CardSlotProps) {
  const diagnostics = useStore((s) => (run.mission_id ? s.missionDetails[run.mission_id]?.diagnostics : undefined)) ?? noDiagnostics
  const navigate = useStore((s) => s.navigate)
  if (!diagnostics.length) return null
  const label = `Swarm conflict warnings: ${diagnostics.length}`
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="link" size="sm" hint={label}>
          <TriangleAlert />
          {diagnostics.length} {diagnostics.length === 1 ? 'conflict' : 'conflicts'}
        </Button>
      </PopoverTrigger>
      <PopoverContent aria-label="Swarm conflict warnings">
        <ul className="flex flex-col gap-2 text-ui">
          {diagnostics.map((diagnostic, index) => (
            <li key={`${diagnostic.kind}-${diagnostic.task_id}-${index}`} className="flex flex-col gap-0.5">
              <Button variant="link" size="sm" onClick={() => navigate('run', { runId: diagnostic.run_id })}>
                {diagnostic.kind.replaceAll('_', ' ')}
                {diagnostic.unavailable ? ' · unavailable' : ''}
              </Button>
              <p className="whitespace-pre-wrap break-words text-ui-sm text-muted">{detailOf(diagnostic)}</p>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  )
}

registerSlot('card:meta', 'mission-diagnostics', SwarmConflicts)
