import { useState } from 'react'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { WorkspaceImportResult, WorkspaceMirrorAuth } from '@/lib/types'
import { useStore } from '@/store'

export function ImportRepositoryDialog({ client, onClose, onImported }: { client: Api; onClose: () => void; onImported: () => void }) {
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
      setResult(current)
      if (current.created) {
        useStore.getState().upsertWorkspace(current.workspace)
        onImported()
      }
    } catch (cause) {
      setError(message(cause))
      setUncertain(true)
      onImported()
    } finally { setBusy(false) }
  }

  if (sourceOpen && result?.created) {
    return <WorkspaceMirrorDialog workspaceID={result.workspace.id} client={client} suggestedSource={source.trim()} onClose={() => { onImported(); onClose() }} />
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
          <p className="text-xs text-muted-foreground">Origin is a separate checkout push destination. It is never inferred from the read-only source; you may use a writable fork or leave it blank.</p>
          <Label className="block space-y-1">Source authentication<select className="h-9 w-full rounded border bg-background px-2" value={auth} onChange={(event) => setAuth(event.target.value as WorkspaceMirrorAuth)}><option value="public">Public HTTPS</option><option value="deploy-key">Read-only deploy key</option></select></Label>
          {auth === 'deploy-key' && <>
            <p className="text-xs text-muted-foreground">Use GitHub HTTPS or a generic ssh:// source. Import generates a public deploy key without fetching. Install it read-only at the source, then Verify / Refresh in Source control. Do not reconfigure after installing the key: reconfiguration rotates it.</p>
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
          <p className="break-all">Accepted base: <code>{result.mirror.accepted_commit || 'None — adoption required before launch'}</code></p>
          {result.mirror.public_key && <><p>Install this read-only deploy key at the source, then continue to Source control to verify it:</p><pre className="overflow-auto whitespace-pre-wrap break-all">{result.mirror.public_key}</pre></>}
          {result.error && <p role="alert" className="whitespace-pre-wrap break-words text-state-failed">{result.error}</p>}
          {result.created && <p>The workspace is retained even if fetch failed. Continue with this workspace's Source control to repair or refresh the source and explicitly adopt the reviewed generation; do not import again.</p>}
        </section>}
      </form>
      <DialogFooter className="border-t pt-3">
        <Button variant="outline" disabled={busy} onClick={onClose}>{result?.created ? 'Close' : 'Cancel'}</Button>
        {result?.created ? <Button onClick={() => setSourceOpen(true)}>Continue to Source control</Button> : <Button type="submit" form="import-repository" disabled={busy || uncertain || !name.trim() || !source.trim() || !base.trim()}>{busy ? 'Importing...' : 'Import repository'}</Button>}
      </DialogFooter>
    </DialogContent>
  </Dialog>
}
