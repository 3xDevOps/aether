import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useDelayed } from '@/lib/hooks'
import { RunMap } from '@/routes/board/run-map'
import { useMapRuns } from '@/routes/board/selectors'
import { useStore } from '@/store'

export function BoardMap({ agentNames }: { agentNames: Record<string, string> }) {
  const workspace = useStore((s) => s.activeWorkspace)
  const workspaceName = useStore((s) => s.workspaces[s.activeWorkspace]?.name ?? s.activeWorkspace)
  const allWorkspaces = useStore((s) => s.boardMapAllWorkspaces)
  const setAllWorkspaces = useStore((s) => s.setBoardMapAllWorkspaces)
  const mineOnly = useStore((s) => s.mineOnly)
  const setMineOnly = useStore((s) => s.setMineOnly)
  const hydrated = useStore((s) => s.hydrated)
  const error = useStore((s) => s.hydrationError)
  const dead = useStore((s) => s.streamDead)
  const scope = allWorkspaces ? '' : workspace
  const { cards, archivedCards } = useMapRuns(scope)
  const [showArchived, setShowArchived] = useState(false)
  useEffect(() => { setShowArchived(false) }, [scope])
  useEffect(() => { if (!archivedCards.length) setShowArchived(false) }, [archivedCards.length])
  const total = cards.length + archivedCards.length
  const loading = useDelayed(!hydrated && error === null && total === 0)
  const visible = showArchived ? [...cards.filter((card) => card.group !== 'finished'), ...archivedCards] : cards

  return (
    <RunMap
      cards={visible}
      scope={JSON.stringify([scope, mineOnly, showArchived])}
      agentNames={agentNames}
      empty={error !== null && total === 0 ? (
        <div className="p-4"><Callout role="alert" tone="failed" title="Cannot reach the server">{dead ? error : 'Retrying.'}</Callout></div>
      ) : loading ? <p role="status" className="p-4 text-ui-sm text-muted">Loading runs…</p> : undefined}
      actions={
        <>
          <Select value={scope ? 'workspace' : 'all'} onValueChange={(value) => setAllWorkspaces(value === 'all')}>
            <SelectTrigger aria-label="Map workspace scope" className="w-40"><SelectValue /></SelectTrigger>
            <SelectContent>
              {workspace && <SelectItem value="workspace">{workspaceName}</SelectItem>}
              <SelectItem value="all">All workspaces</SelectItem>
            </SelectContent>
          </Select>
          <Button variant={mineOnly ? 'secondary' : 'ghost'} size="sm" aria-pressed={mineOnly} onClick={() => setMineOnly(!mineOnly)} hint="Mine filters Working and Finished. Needs you always includes every workspace.">Mine</Button>
          {(archivedCards.length > 0 || showArchived) && (
            <Button variant="ghost" size="sm" aria-pressed={showArchived} onClick={() => setShowArchived(!showArchived)}>
              {showArchived ? 'Back to Finished' : `Archived (${archivedCards.length})`}
            </Button>
          )}
        </>
      }
    />
  )
}
