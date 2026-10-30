import { memo, useLayoutEffect, useRef } from 'react'
import { Avatar } from '@/components/ui/avatar'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { MapNodeCard } from '@/routes/board/map-card'
import { MapConnectors } from '@/routes/board/map-connectors'
import { captionHeight, sectionHeader, type MapNode, type MapSection, type MapUnit, type RunMapLayout } from '@/routes/board/map-layout'

type FoldHandler = (unitKey: string, folded: boolean) => void

// Labels grow by `--map-label-scale` as the camera zooms out, so section and
// caption names stay readable over cards too small to read.
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
          aria-label={section.kind === 'visiting' ? section.label : `${section.label}'s sessions`}
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
      <span className="ml-auto shrink-0 rounded-chip bg-chrome px-1 text-ui-sm text-muted tabular-nums">{section.count} {section.count === 1 ? 'session' : 'sessions'}</span>
    </header>
  )
}

function Unit({ unit, section, agentNames, onFold }: {
  unit: MapUnit
  section: MapSection
  agentNames: Record<string, string>
  onFold: FoldHandler
}) {
  const childRuns = `${unit.childRuns} ${unit.childRuns === 1 ? 'subsession' : 'subsessions'}`
  const childrenId = `map-children-${unit.key}`
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
        fold={entry.role === 'parent' && unit.childRuns > 0 ? {
          unitKey: unit.key,
          childRuns: unit.childRuns,
          hidden: unit.hidden.length,
          hiddenNeedsYou: unit.hidden.filter((card) => card.group === 'needs-you').length,
          controls: childrenId,
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
      aria-label={unit.kind === 'standalone' ? 'Standalone session' : `Session with subsessions: ${runLabel(unit.nodes[0].card.run)}, ${childRuns}`}
      className="absolute"
      style={{ left: unit.x - section.x, top: unit.y - section.y, width: unit.width, height: unit.height }}
    >
      {unit.kind !== 'standalone' && (
        <p
          className="absolute top-0 left-1/2 origin-bottom -translate-x-1/2 truncate rounded-chip bg-chrome px-1 text-center text-ui-sm leading-5 text-muted"
          style={{ height: captionHeight - 8, ...scaled(unit.width, captionScale) }}
        >
          {childRuns}
        </p>
      )}
      {node(unit.nodes[0])}
      <div id={childrenId}>{unit.nodes.slice(1).map(node)}</div>
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
