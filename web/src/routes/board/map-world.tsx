import { memo, useLayoutEffect, useRef } from 'react'
import { Avatar } from '@/components/ui/avatar'
import { cn } from '@/lib/utils'
import { MapNodeCard } from '@/routes/board/map-card'
import { MapConnectors } from '@/routes/board/map-connectors'
import { captionHeight, sectionHeader, type MapNode, type MapSection, type MapUnit, type RunMapLayout } from '@/routes/board/map-layout'
import { phaseWord } from '@/routes/board/run-card'
import { useStore } from '@/store'

type FoldHandler = (unitKey: string, folded: boolean) => void

// Labels grow by `--map-label-scale` as the camera zooms out, so section and
// swarm names stay readable over cards too small to read.
const scaled = (width: number, scale = 'var(--map-label-scale)') => ({ width: `calc(${width}px / ${scale})`, scale })
// A 20px caption over the 24px shelf gap has room to grow 2.2 times before it reaches the cards above.
const captionScale = 'min(var(--map-label-scale), 2.2)'

/** Everything inside the camera; it re-renders on layout changes, never on a pan or zoom. */
export const MapWorld = memo(function MapWorld({ layout, agentNames, ghosts, onFold, fadeGhost }: {
  layout: RunMapLayout
  agentNames: Record<string, string>
  ghosts: MapNode[]
  onFold: FoldHandler
  fadeGhost: (el: HTMLElement, key: string) => void
}) {
  return (
    <>
      {layout.sections.map((section) => (
        <section
          key={section.key}
          aria-label={section.kind === 'visiting' ? section.label : `${section.label}'s runs`}
          data-glide-key={`s:${section.key}`}
          className={cn('absolute', section.framed && 'rounded-panel border border-seam')}
          style={{ left: section.x, top: section.y, width: section.width, height: section.height }}
        >
          {section.framed && <SectionHeader section={section} />}
          {section.units.map((unit) => (
            <Unit key={unit.key} unit={unit} section={section} agentNames={agentNames} onFold={onFold} />
          ))}
        </section>
      ))}
      {ghosts.length > 0 && (
        <div aria-hidden inert className="pointer-events-none">
          {ghosts.map((node) => <Ghost key={node.key} node={node} agentNames={agentNames} fadeGhost={fadeGhost} />)}
        </div>
      )}
    </>
  )
})

function SectionHeader({ section }: { section: MapSection }) {
  return (
    <header
      className="pointer-events-none absolute top-0 left-0 z-10 flex min-w-0 origin-top-left items-center gap-2 px-6"
      style={{ height: sectionHeader, ...scaled(section.width) }}
    >
      {section.kind === 'owner' && <Avatar name={section.label} color={section.member?.color} />}
      <h3 className="truncate rounded-chip bg-chrome px-1 text-ui font-medium">{section.label}</h3>
      <span className="ml-auto shrink-0 rounded-chip bg-chrome px-1 text-ui-sm text-muted tabular-nums">{section.count} {section.count === 1 ? 'run' : 'runs'}</span>
    </header>
  )
}

function Unit({ unit, section, agentNames, onFold }: {
  unit: MapUnit
  section: MapSection
  agentNames: Record<string, string>
  onFold: FoldHandler
}) {
  const mission = useStore((s) => (unit.missionId ? s.missions[unit.missionId] : undefined))
  const caption = unit.kind === 'orphan'
    ? 'Integrator not visible'
    : mission ? `${mission.objective} · ${phaseWord[mission.phase]}` : 'Swarm'
  const workersId = `map-workers-${unit.key}`
  const root = unit.kind === 'orphan' ? undefined : unit.nodes[0]
  const workers = root ? unit.nodes.slice(1) : unit.nodes
  const node = (entry: MapNode) => (
    <div
      key={entry.key}
      data-map-node
      data-map-role={entry.role}
      data-glide-key={`n:${entry.key}`}
      className="absolute"
      style={{ left: entry.x - unit.x, top: entry.y - unit.y, width: entry.width, height: entry.height }}
    >
      <MapNodeCard
        card={entry.card}
        agentName={agentNames[entry.card.run.harness]}
        showOwner={entry.showOwner}
        fold={entry.role === 'integrator' && unit.workers > 0 ? {
          unitKey: unit.key,
          workers: unit.workers,
          hidden: unit.hidden.length,
          hiddenNeedsYou: unit.hidden.filter((card) => card.group === 'needs-you').length,
          controls: workersId,
          onFold,
        } : undefined}
      />
    </div>
  )
  return (
    <div
      data-map-unit
      data-glide-key={`u:${unit.key}`}
      role="group"
      aria-label={unit.kind === 'standalone' ? 'Standalone run'
        : unit.kind === 'orphan' ? `Workers, integrator not visible, ${unit.nodes.length} ${unit.nodes.length === 1 ? 'worker' : 'workers'}`
          : `Swarm: ${mission?.objective ?? unit.missionId}, ${unit.workers} ${unit.workers === 1 ? 'worker' : 'workers'}`}
      className="absolute"
      style={{ left: unit.x - section.x, top: unit.y - section.y, width: unit.width, height: unit.height }}
    >
      {unit.kind !== 'standalone' && (
        <p
          title={unit.missionId}
          className="absolute top-0 left-1/2 origin-bottom -translate-x-1/2 truncate rounded-chip bg-chrome px-1 text-center text-ui-sm leading-5 text-muted"
          style={{ height: captionHeight - 8, ...scaled(unit.width, captionScale) }}
        >
          {caption}
        </p>
      )}
      {root && node(root)}
      <div id={workersId}>{workers.map(node)}</div>
      <MapConnectors unit={unit} />
    </div>
  )
}

function Ghost({ node, agentNames, fadeGhost }: {
  node: MapNode
  agentNames: Record<string, string>
  fadeGhost: (el: HTMLElement, key: string) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    if (ref.current) fadeGhost(ref.current, node.key)
  }, [fadeGhost, node.key])
  return (
    <div ref={ref} className="absolute" style={{ left: node.x, top: node.y, width: node.width, height: node.height }}>
      <MapNodeCard card={node.card} agentName={agentNames[node.card.run.harness]} showOwner={node.showOwner} />
    </div>
  )
}
