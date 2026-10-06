import { useLayoutEffect, useRef, useState } from 'react'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { Workspace, WorkspaceImportResult, WorkspaceMirrorAuth } from '@/lib/types'
import { useStore } from '@/store'

export function ImportRepositoryDialog({ client, onClose, onImported }: { client: Api; onClose: () => void; onImported: (workspace?: Workspace) => void }) {
  const [name, setName] = useState('')
  const [source, setSource] = useState('')
  const [base, setBase] = useState('main')
  const [origin, setOrigin] = useState('')
  const [auth, setAuth] = useState<WorkspaceMirrorAuth>('public')
  const [knownHosts, setKnownHosts] = useState('')
  const [result, setResult] = useState<WorkspaceImportResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [uncertain, setUncertain] = useState(false)
  const [sourceOpen, setSourceOpen] = useState(false)
  const generation = useRef(0)

  useLayoutEffect(() => {
    const reset = () => {
      setName('')
      setSource('')
      setBase('main')
      setOrigin('')
      setAuth('public')
      setKnownHosts('')
      setResult(null)
      setBusy(false)
      setError(null)
      setUncertain(false)
      setSourceOpen(false)
    }
    reset()
    const unsubscribe = useStore.subscribe((state, previous) => {
      if (state.identityKey === previous.identityKey && state.connectionEpoch === previous.connectionEpoch) return
      generation.current += 1
      reset()
    })
    return () => { generation.current += 1; unsubscribe() }
  }, [client])

  const version = generation.current
  const { identityKey, connectionEpoch } = useStore.getState()
  const isCurrent = () => {
    const state = useStore.getState()
    return version === generation.current && identityKey === state.identityKey && connectionEpoch === state.connectionEpoch
  }

  async function submit() {
    if (busy || result?.created || uncertain) return
    setBusy(true)
    setError(null)
    try {
      const current = await client.workspaceImport({
        name: name.trim(), environment: {}, source_url: source.trim(),
        base_branch: base.trim(), origin: origin.trim(), auth,
        ...(auth === 'deploy-key' && knownHosts.trim() ? { known_hosts: knownHosts } : {}),
      })
      if (!isCurrent()) return
      setResult(current)
      if (current.created) {
        useStore.getState().upsertWorkspace(current.workspace)
        if (isCurrent()) onImported(current.workspace)
      }
    } catch (cause) {
      if (!isCurrent()) return
      setError(message(cause))
      setUncertain(!(cause instanceof ApiError && (cause.code === -32602 || cause.code === -32001)))
      onImported()
    } finally {
      if (isCurrent()) setBusy(false)
    }
  }

  if (sourceOpen && result?.created) {
    return <WorkspaceMirrorDialog workspaceID={result.workspace.id} client={client} suggestedSource={source.trim()} onClose={() => { if (!isCurrent()) return; onImported(result.workspace); if (isCurrent()) onClose() }} />
  }

  return <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose() }}>
    <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(640px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
      <DialogHeader><DialogTitle>Import repository</DialogTitle><DialogDescription>Create a workspace from a server-fetched source. No local clone is needed. Fetching is not approval: adopt the observed candidate explicitly.</DialogDescription></DialogHeader>
      <form id="import-repository" className="min-h-0 space-y-3 overflow-y-auto px-1" onSubmit={(event) => { event.preventDefault(); void submit() }}>
        <fieldset disabled={busy || !!result?.created || uncertain} className="space-y-3">
          <Label className="block space-y-1">Workspace name<Input value={name} onChange={(event) => setName(event.target.value)} required /></Label>
          <Label className="block space-y-1">Source URL<Input value={source} onChange={(event) => setSource(event.target.value)} placeholder="https://github.com/upstream/repository.git" required /></Label>
          <Label className="block space-y-1">Source / base branch<Input value={base} onChange={(event) => setBase(event.target.value)} required /></Label>
          <Label className="block space-y-1">Checkout Origin (optional)<Input value={origin} onChange={(event) => setOrigin(event.target.value)} placeholder="Leave blank for no push destination" /></Label>
          <p className="text-xs text-muted-foreground">Use credential-free URLs. Origin is a separate checkout push destination, never inferred from the read-only source. Publishing requires your own native Git/gh credentials and upstream permission; a deploy key here grants read access only. Use a writable fork or leave Origin blank.</p>
          <Label className="block space-y-1">Source authentication<select className="h-9 w-full rounded border bg-background px-2" value={auth} onChange={(event) => setAuth(event.target.value as WorkspaceMirrorAuth)}><option value="public">Public HTTPS</option><option value="deploy-key">Read-only deploy key</option></select></Label>
          {auth === 'deploy-key' && <>
            <p className="text-xs text-muted-foreground">Use GitHub HTTPS or a generic ssh:// source. Import generates a public deploy key without fetching. A repository administrator must install it read-only at the source, then Verify / Refresh on the Repository page and explicitly adopt the candidate. Generic SSH requires known_hosts verified with the host administrator, not a blindly trusted scan. Do not reconfigure after installing the key: reconfiguration rotates it.</p>
            <Label className="block space-y-1">Pinned known_hosts (required for generic SSH)<Textarea value={knownHosts} onChange={(event) => setKnownHosts(event.target.value)} className="font-mono text-xs" /></Label>
          </>}
        </fieldset>
        {error && <p role="alert" className="whitespace-pre-wrap break-words text-xs text-state-failed">{error}</p>}
        {uncertain && <p role="alert" className="text-xs">The response did not establish whether a workspace was created. Close this dialog and inspect the refreshed workspace list before considering another import. Do not blindly repeat creation.</p>}
        {result && <section aria-label="Import outcome" className="space-y-2 border p-3 text-xs">
          <p>Created: {result.created ? 'yes' : 'no'}</p>
          {result.created && <p className="break-all">Retained workspace: <strong>{result.workspace.name}</strong> · <code>{result.workspace.id}</code></p>}
          <p>Source state: {result.mirror.status ?? (result.mirror.enabled ? 'Unknown' : 'Not configured')}</p>
          <p className="break-all">Observed candidate: <code>{result.mirror.observed_commit || 'None'}</code></p>
          <p>Candidate generation: {result.mirror.generation ?? 'Unavailable'}</p>
          <p className="break-all">Accepted base: <code>{result.mirror.accepted_commit || 'None - adoption required before launch'}</code></p>
          {result.mirror.public_key && <><p>Install this read-only deploy key at the source, then continue to the Repository page to verify it:</p><pre className="overflow-auto whitespace-pre-wrap break-all">{result.mirror.public_key}</pre></>}
          {result.error && <p role="alert" className="whitespace-pre-wrap break-words text-state-failed">{result.error}</p>}
          {result.created && <p>The workspace is retained even if fetch failed. Continue on this workspace's Repository page to repair or refresh the source and explicitly adopt the reviewed generation; do not import again.</p>}
        </section>}
      </form>
      <DialogFooter className="border-t pt-3">
        <Button variant="secondary" disabled={busy} onClick={onClose}>{result?.created ? 'Close' : 'Cancel'}</Button>
        {result?.created ? <Button onClick={() => setSourceOpen(true)}>Continue to Repository</Button> : <Button type="submit" form="import-repository" disabled={busy || uncertain || !name.trim() || !source.trim() || !base.trim()}>{busy ? 'Importing…' : 'Import repository'}</Button>}
      </DialogFooter>
    </DialogContent>
  </Dialog>
}
