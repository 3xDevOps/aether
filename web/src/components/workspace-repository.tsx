import { useState, type ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, type Api } from '@/lib/api'
import { shellQuote } from '@/lib/shell'
import type { Workspace, WorkspaceMirrorResult } from '@/lib/types'
import { RepoStep } from '@/routes/onboarding/repo-step'
import { OnboardingSourceOption } from '@/routes/onboarding/source-option'
import { useCapability, useIsAdmin, type Capability } from '@/store/hooks'

export function WorkspaceRepository({ client = api, caps, workspace, initialLocal = false, onLocalChange, back, onNext }: {
  client?: Api
  caps: Capability
  workspace: Workspace
  initialLocal?: boolean
  onLocalChange?: (local: boolean) => void
  back?: ReactNode
  onNext?: () => void
}) {
  const isAdmin = useIsAdmin()
  const [local, setLocal] = useState(initialLocal)
  const [source, setSource] = useState<WorkspaceMirrorResult | null>(null)
  const canMirror = isAdmin && caps.hasMethod('workspace.mirror.status')

  return <section aria-label="Workspace repository" className="space-y-4 py-4">
    <div className="space-y-1">
      <h2 className="text-base font-semibold">Repository for {workspace.name}</h2>
      <p className="text-sm text-muted-foreground">Workspace <code>{workspace.id}</code> · base branch <code>{workspace.base_branch}</code>. Runs need this branch on the server, not just an empty workspace.</p>
    </div>
    {canMirror ? <OnboardingSourceOption client={client} workspaceID={workspace.id} onStatusChange={setSource} /> : <p className="text-sm text-muted-foreground">An administrator manages public/private remote sources and candidate adoption in Source control. Ask them to verify the source and accepted base before launching.</p>}
    <div className="space-y-2 border-t pt-3">
      <h3 className="text-sm font-semibold">Local clone</h3>
      <p className="text-xs leading-5 text-muted-foreground">Link or relink a clone to this workspace. Linking changes its aether remote, not its origin or history. A mirrored base stays server-owned.</p>
      {!local && <Button size="sm" variant="outline" onClick={() => { setLocal(true); onLocalChange?.(true) }}>Link local repository</Button>}
      {local && <RepoStep client={client} caps={caps} workspace={workspace} mirrored={source?.enabled === true} sourcePending={canMirror && source === null} back={<>{back}<Button size="sm" variant="outline" onClick={() => { setLocal(false); onLocalChange?.(false) }}>Back to repository choices</Button></>} onNext={onNext ?? (() => { setLocal(false); onLocalChange?.(false) })} />}
    </div>
    <div className="space-y-2 border-t pt-3 text-sm">
      <h3 className="font-semibold">Checkout Origin</h3>
      <p className="break-all font-mono text-xs">{workspace.origin || 'Not configured; new run checkouts have no publishing remote.'}</p>
      <p className="text-xs text-muted-foreground">Origin is the upstream URL for new run checkouts, not the read-only mirror source. To change it deliberately, run this on your linked computer with the intended URL:</p>
      <pre className="overflow-auto whitespace-pre-wrap break-words bg-muted p-3 text-xs">{`aether workspace origin --workspace ${shellQuote(workspace.id)} https://your-git-host/your-team/your-repository.git`}</pre>
    </div>
    {!local && <div className="flex flex-wrap gap-2 border-t pt-3">
      {onNext && <Button size="sm" onClick={onNext}>Continue to agents</Button>}
      {back}
    </div>}
    <p className="text-xs leading-5 text-muted-foreground">A read-only deploy key only lets the server fetch. To publish branches or pull requests, configure your own native Git/gh credentials in your environment terminal and obtain upstream permission. Agent vendor login and Git author identity are separate.</p>
  </section>
}

export function WorkspaceRepositoryDialog({ workspace, client = api, onClose, initialLocal = false }: {
  workspace: Workspace
  client?: Api
  onClose: () => void
  initialLocal?: boolean
}) {
  const caps = useCapability()
  return <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
    <DialogContent className="grid-cols-1 max-h-[calc(100dvh-2rem)] max-w-[min(800px,calc(100%-2rem))] overflow-y-auto">
      <DialogHeader><DialogTitle>Workspace repository</DialogTitle><DialogDescription>{workspace.name} · source and local clone settings</DialogDescription></DialogHeader>
      <WorkspaceRepository key={workspace.id} client={client} caps={caps} workspace={workspace} initialLocal={initialLocal} />
      <Button size="sm" variant="outline" onClick={onClose}>Close repository settings</Button>
    </DialogContent>
  </Dialog>
}
