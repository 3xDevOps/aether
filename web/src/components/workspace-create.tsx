import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Code, CodeBlock } from '@/components/ui/code'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import { shellQuote } from '@/lib/shell'
import { cn } from '@/lib/utils'
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

  const canImport = isAdmin && caps.hasMethod('workspace.import')
  const canAdd = isAdmin && caps.hasMethod('workspace.add')
  if (!canImport && !canAdd) return null

  return <section aria-label="Add workspace" className="flex min-w-0 flex-col gap-3">
    <h3 className="text-ui font-medium text-text">Add a workspace</h3>
    <div className="grid gap-3 sm:grid-cols-2">
      <div className="flex flex-col items-start gap-2 rounded-panel border border-seam p-3">
        <h4 className="text-ui font-medium text-text">Import a remote repository</h4>
        <p className="flex-1 text-ui-sm text-muted">Public HTTPS, or private with a read-only deploy key. No local clone needed.</p>
        {canImport
          ? <Button size="sm" variant="secondary" disabled={busy || uncertain} onClick={() => { setImported(null); setChoice('remote') }}>Import repository</Button>
          : <p className="text-ui-sm text-muted">This gateway cannot import repositories.</p>}
      </div>
      <div className={cn('flex flex-col items-start gap-2 rounded-panel border p-3', choice === 'local' ? 'border-accent' : 'border-seam')}>
        <h4 className="text-ui font-medium text-text">From a local clone</h4>
        <p className="flex-1 text-ui-sm text-muted">Create the workspace, then link your clone and push its base branch. History is never rewritten.</p>
        {canAdd
          ? <Button size="sm" variant="secondary" disabled={busy || uncertain} onClick={() => setChoice('local')}>Create from local clone</Button>
          : <p className="text-ui-sm text-muted">This gateway cannot create workspaces.</p>}
      </div>
    </div>
    {choice === 'local' && (caps.hasLocal('link.repo') ? <form aria-label="Create workspace" className="flex flex-col gap-3 border-t border-seam pt-3" onSubmit={(event) => { event.preventDefault(); void create() }}>
      <fieldset disabled={busy || uncertain} className="grid gap-3 sm:grid-cols-2">
        <FormField label="Workspace name"><Input value={name} onChange={(event) => setName(event.target.value)} required /></FormField>
        <FormField label="Base branch" help="The branch that already exists in your clone. Nothing is uploaded until you push it."><Input value={base} onChange={(event) => setBase(event.target.value)} required /></FormField>
      </fieldset>
      {error && <Callout tone="failed" role="alert" className="whitespace-pre-wrap">{error}</Callout>}
      {uncertain && <Callout tone="needs-you" role="alert">Creation could not be confirmed. Inspect the refreshed workspace list before trying again; if it exists, open it and link your clone there.</Callout>}
      <div><Button type="submit" size="sm" disabled={busy || uncertain || !name.trim() || !base.trim()}>{busy ? 'Creating...' : 'Create workspace'}</Button></div>
    </form> : <div className="flex flex-col gap-2 border-t border-seam pt-3 text-ui">
      <p>This hosted gateway cannot reach your clone. On the computer holding it, open the desktop app or run <Code>aether gui</Code> connected to this server, then choose <strong>Create from local clone</strong>.</p>
      <p className="text-ui-sm text-muted">From a terminal instead, replace <Code>&lt;server-address-or-id&gt;</Code> with this server&apos;s SSH address or server id from your administrator, and the name, branch and path below. Creating a workspace requires an admin.</p>
      <CodeBlock className="whitespace-pre-wrap break-words">{`aether link ${shellQuote('<server-address-or-id>')} &&\naether workspace add myproject --base main &&\naether link ${shellQuote('<server-address-or-id>')} --repo /absolute/path/to/clone --workspace myproject &&\ngit -C /absolute/path/to/clone push -u aether main`}</CodeBlock>
    </div>)}
    {choice === 'remote' && <ImportRepositoryDialog client={client} onImported={(workspace) => { if (workspace) setImported(workspace); onRefresh() }} onClose={() => { setChoice(null); if (imported) onCreated(imported, 'remote') }} />}
  </section>
}
