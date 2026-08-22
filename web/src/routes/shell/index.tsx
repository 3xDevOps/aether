// The workspace-shell route: renders whatever shell the store was asked to
// open (workspace bootstrap, harness login, agent setup) behind one xterm
// pane. The request lives in the ShellSlice so any surface can open a shell
// with a single openShell call and a navigate.

import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { ViewHeader } from '@/components/view-header'
import { registerRoute } from '@/routes/registry'
import { ShellPane } from '@/routes/shell/pane'
import { useStore } from '@/store'

function ShellView() {
  const req = useStore((s) => s.shellRequest)
  const navigate = useStore((s) => s.navigate)
  const [exited, setExited] = useState(false)

  // Resume/Reset replace the request with a fresh one: the pane remounts
  // and reconnects, so the "where to go next" footer from the previous
  // exit must not stay rendered over the live shell.
  useEffect(() => {
    setExited(false)
  }, [req])

  if (!req) {
    return (
      <div className="flex h-full flex-col">
        <ViewHeader title="Workspace shell" />
        <div className="flex flex-1 flex-col items-center justify-center gap-3 p-4">
          <p className="text-sm text-muted-foreground">
            No shell is open. Start one from a workspace or the agent wizard.
          </p>
          <Button variant="outline" onClick={() => navigate('board')}>
            Back to board
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <ViewHeader title="Workspace shell" subtitle={req.mode} />
      <div className="min-h-0 flex-1">
        {/* The pane keeps its own outcome banner mounted after the exit;
            this route only adds where to go next. */}
        <ShellPane req={req} onExit={() => setExited(true)} />
      </div>
      {exited && (
        <div className="flex items-center gap-3 border-t px-4 py-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => navigate('workspaces')}
          >
            Back to workspaces
          </Button>
          {req.mode === 'agent-setup' && (
            <Button size="sm" variant="outline" onClick={() => navigate('agents')}>
              Back to agents
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

registerRoute('shell', ShellView)
