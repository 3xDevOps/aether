import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { shellQuote } from '@/lib/shell'
import type { Workspace } from '@/lib/types'
import { ImportRepositoryDialog } from '@/routes/admin-dialogs/import-repository-dialog'
import { useStore } from '@/store'
import { useCapability, useIsAdmin } from '@/store/hooks'

export function WorkspaceCreate({ client, onCreated, onRefresh }: {
  client: Api
  onCreated: (workspace: Workspace, source: 'local' | 'remote') => void
  onRefresh: () => void
}) {
  const caps = useCapability()
  const isAdmin = useIsAdmin()
  const [choice, setChoice] = useState<'local' | 'remote' | null>(null)
  const [imported, setImported] = useState<Workspace | null>(null)
  const [name, setName] = useState('')
  const [base, setBase] = useState('main')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [uncertain, setUncertain] = useState(false)
  const generation = useRef(0)

  useEffect(() => {
    setBusy(false)
    const unsubscribe = useStore.subscribe((state, previous) => {
      if (state.identityKey === previous.identityKey && state.connectionEpoch === previous.connectionEpoch && state.route === previous.route) return
      generation.current += 1
      setChoice(null)
      setImported(null)
      setName('')
      setBase('main')
      setBusy(false)
      setError(null)
      setUncertain(false)
    })
    return () => { generation.current += 1; unsubscribe() }
  }, [client])

  const create = async () => {
    if (busy || uncertain) return
    const version = generation.current
    setBusy(true)
    setError(null)
    try {
      const workspace = await client.workspaceAdd({ name: name.trim(), base_branch: base.trim(), environment: {} })
      if (version !== generation.current) return
      useStore.getState().upsertWorkspace(workspace)
      if (version === generation.current) onCreated(workspace, 'local')
    } catch (cause) {
      if (version !== generation.current) return
      setError(message(cause))
      setUncertain(!(cause instanceof ApiError && (cause.code === -32602 || cause.code === -32001)))
      onRefresh()
    } finally {
      if (version === generation.current) setBusy(false)
    }
  }

  return <section aria-label="Add workspace" className="space-y-3 border-y py-4">
    <div className="space-y-1">
      <h2 className="text-base font-semibold">Add a workspace</h2>
      <p className="text-sm text-muted-foreground">A workspace shares one repository and base branch across its runs. Choose where its code comes from.</p>
    </div>
    <div className="grid gap-3 sm:grid-cols-2">
      <div className="space-y-2 border p-3">
        <h3 className="text-sm font-semibold">Public or private remote repository</h3>
        <p className="text-xs leading-5 text-muted-foreground">Import public HTTPS or a private repository with a read-only deploy key. No local clone is needed.</p>
        {isAdmin && caps.hasMethod('workspace.import') ? <Button size="sm" variant="outline" disabled={busy || uncertain} onClick={() => { setImported(null); setChoice('remote') }}>Import repository</Button> : <p className="text-xs text-muted-foreground">An administrator with repository import access must create the workspace.</p>}
      </div>
      <div className="space-y-2 border p-3">
        <h3 className="text-sm font-semibold">Local clone</h3>
        <p className="text-xs leading-5 text-muted-foreground">Create a workspace, link your existing Git clone, then push its base branch without rewriting history.</p>
        {isAdmin && caps.hasMethod('workspace.add') ? <Button size="sm" variant="outline" disabled={busy || uncertain} onClick={() => setChoice('local')}>Create from local clone</Button> : <p className="text-xs text-muted-foreground">An administrator must create the workspace. You can link your clone to an existing workspace below.</p>}
      </div>
    </div>
    {choice === 'local' && (caps.hasLocal('link.repo') ? <form aria-label="Create workspace" className="space-y-3 border-t pt-3" onSubmit={(event) => { event.preventDefault(); void create() }}>
      <fieldset disabled={busy || uncertain} className="grid gap-3 sm:grid-cols-2">
        <Label className="block space-y-1">Workspace name<Input value={name} onChange={(event) => setName(event.target.value)} required /></Label>
        <Label className="block space-y-1">Base branch<Input value={base} onChange={(event) => setBase(event.target.value)} required /></Label>
      </fieldset>
      <p className="text-xs text-muted-foreground">Use the branch that already exists in your clone. Creating the workspace does not upload code; the next screen links the clone and pushes this branch.</p>
      {error && <p role="alert" className="whitespace-pre-wrap break-words text-sm text-state-failed">{error}</p>}
      {uncertain && <p role="alert" className="text-sm">Creation could not be confirmed. Inspect the refreshed workspace list before trying again; if it exists, open it and link your clone there.</p>}
      <Button type="submit" size="sm" disabled={busy || uncertain || !name.trim() || !base.trim()}>{busy ? 'Creating...' : 'Create workspace'}</Button>
    </form> : <div className="space-y-2 border-t pt-3 text-sm">
      <p>This hosted gateway cannot access your clone. On the computer holding it, open the desktop app or run <code>aether gui</code>, connected to this server, then choose <strong>Create from local clone</strong>.</p>
      <p className="text-xs text-muted-foreground">CLI alternative: replace <code>&lt;server-address-or-id&gt;</code> with this server's SSH address (including its SSH port) or server ID from your administrator, not this page's HTTP address. This hosted gateway does not expose that connection target. Replace the name, branch and absolute path below. Workspace creation requires an admin.</p>
      <pre className="overflow-auto whitespace-pre-wrap break-words bg-muted p-3 text-xs">{`aether link ${shellQuote('<server-address-or-id>')} &&\naether workspace add myproject --base main &&\naether link ${shellQuote('<server-address-or-id>')} --repo /absolute/path/to/clone --workspace myproject &&\ngit -C /absolute/path/to/clone push -u aether main`}</pre>
    </div>)}
    {choice === 'remote' && <ImportRepositoryDialog client={client} onImported={(workspace) => { if (workspace) setImported(workspace); onRefresh() }} onClose={() => { setChoice(null); if (imported) onCreated(imported, 'remote') }} />}
  </section>
}
