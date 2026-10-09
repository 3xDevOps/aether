import { useLayoutEffect, useRef, useState } from 'react'
import { GitHubConnection } from '@/components/github-connection'
import { WorkspaceMirrorDialog } from '@/components/workspace-mirror-dialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ListRow } from '@/components/ui/list-row'
import { ApiError, api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { GitHubAccount, GitHubRepository, Workspace, WorkspaceMirrorResult } from '@/lib/types'
import { useStore } from '@/store'
import { useIsAdmin } from '@/store/hooks'

export function GitHubRepositoryDialog({ client = api, onCreated, onClose }: {
  client?: Api
  onCreated: (workspace: Workspace) => void
  onClose: () => void
}) {
  const admin = useIsAdmin()
  const [connected, setConnected] = useState(false)
  const [account, setAccount] = useState<GitHubAccount | null>(null)
  const [repositories, setRepositories] = useState<GitHubRepository[]>([])
  const [nextPage, setNextPage] = useState<number | undefined>()
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState<GitHubRepository | null>(null)
  const [name, setName] = useState('')
  const [branch, setBranch] = useState('')
  const [origin, setOrigin] = useState('')
  const [workspace, setWorkspace] = useState<Workspace | null>(null)
  const [mirror, setMirror] = useState<WorkspaceMirrorResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [uncertain, setUncertain] = useState(false)
  const [sourceOpen, setSourceOpen] = useState(false)
  const generation = useRef(0)
  const listGeneration = useRef(0)
  const mutation = useRef(false)
  const reviewed = useRef<string | null>(null)

  useLayoutEffect(() => {
    const reset = () => {
      generation.current += 1
      listGeneration.current += 1
      mutation.current = false
      reviewed.current = null
      setConnected(false); setAccount(null); setRepositories([]); setNextPage(undefined)
      setQuery(''); setSelected(null); setName(''); setBranch(''); setOrigin('')
      setWorkspace(null); setMirror(null); setLoading(false); setBusy(false)
      setError(null); setUncertain(false); setSourceOpen(false)
    }
    reset()
    const unsubscribe = useStore.subscribe((state, previous) => {
      if (state.identityKey !== previous.identityKey || state.connectionEpoch !== previous.connectionEpoch || state.info?.member.id !== previous.info?.member.id || state.info?.member.role !== previous.info?.member.role) reset()
    })
    return () => { generation.current += 1; unsubscribe() }
  }, [client])

  function fence() {
    const version = generation.current
    const { identityKey, connectionEpoch, info } = useStore.getState()
    return () => {
      const state = useStore.getState()
      return version === generation.current && identityKey === state.identityKey && connectionEpoch === state.connectionEpoch && info?.member.id === state.info?.member.id && state.info?.member.role === 'admin'
    }
  }

  async function load(page?: number) {
    const current = fence()
    const request = ++listGeneration.current
    setLoading(true)
    setError(null)
    try {
      const result = await client.githubRepositories(page)
      if (!current() || request !== listGeneration.current) return
      if (page && account && result.account.id !== account.id) {
        setAccount(null); setRepositories([]); setSelected(null); setNextPage(undefined)
        setError('The GitHub account changed. Reload repositories and select a repository using the current account.')
        return
      }
      setAccount(result.account)
      setRepositories((previous) => page ? [...previous, ...result.repositories.filter((repo) => !previous.some((item) => item.id === repo.id))] : result.repositories)
      setNextPage(result.next_page)
      if (!page) { setSelected(null); setName(''); setBranch(''); setOrigin('') }
    } catch (cause) {
      if (current() && request === listGeneration.current) setError(message(cause))
    } finally {
      if (current() && request === listGeneration.current) setLoading(false)
    }
  }

  async function importRepository() {
    if (mutation.current || workspace || uncertain || !selected || !account) return
    const current = fence()
    mutation.current = true
    setBusy(true); setError(null)
    try {
      const result = await client.workspaceImport({
        name: name.trim(), environment: {}, source_url: selected.clone_url,
        base_branch: branch.trim(), origin, auth: 'github', github_account_id: account.id,
      })
      if (!current()) return
      setMirror(result.mirror)
      setError(result.error ?? result.mirror.last_error ?? null)
      if (result.created) {
        setWorkspace(result.workspace)
        useStore.getState().upsertWorkspace(result.workspace)
      } else if (!result.error) setError('The repository import did not create a workspace.')
    } catch (cause) {
      if (!current()) return
      setError(message(cause))
      setUncertain(!(cause instanceof ApiError && (cause.code === -32602 || cause.code === -32001 || cause.code === -32002)))
    } finally {
      if (current()) { mutation.current = false; setBusy(false) }
    }
  }

  async function updateSource(action: 'refresh' | 'adopt' | 'status') {
    if (!workspace || mutation.current) return
    if (action === 'adopt' && (!mirror?.generation || !mirror.observed_commit)) return
    const current = fence()
    if (action === 'adopt' && mirror?.status === 'ready' && mirror.accepted_commit === mirror.observed_commit) {
      if (current()) onCreated(workspace)
      return
    }
    mutation.current = true
    setBusy(true); setError(null)
    if (action === 'adopt') reviewed.current = mirror!.observed_commit!
    try {
      const result = action === 'refresh' ? await client.workspaceMirrorRefresh(workspace.id)
        : action === 'adopt' ? await client.workspaceMirrorAdopt(workspace.id, mirror!.generation!, mirror!.observed_commit!)
          : await client.workspaceMirrorStatus(workspace.id)
      if (!current()) return
      setMirror(result)
      setUncertain(false)
      setError(result.last_error ?? null)
      if (action !== 'refresh' && result.status === 'ready' && result.accepted_commit && result.accepted_commit === reviewed.current) {
        onCreated(workspace)
      }
    } catch (cause) {
      if (!current()) return
      setError(message(cause))
      setUncertain(true)
    } finally {
      if (current()) { mutation.current = false; setBusy(false) }
    }
  }

  if (!admin) return null
  if (sourceOpen && workspace) return <WorkspaceMirrorDialog client={client} workspaceID={workspace.id} onStatusChange={setMirror} onClose={() => { setSourceOpen(false); setUncertain(true); void updateSource('status') }} />
  const visible = repositories.filter((repo) => repo.full_name.toLowerCase().includes(query.trim().toLowerCase()))
  const canAdopt = !!mirror?.enabled && !!mirror.observed_commit && !!mirror.generation && mirror.status !== 'refreshing' && mirror.status !== 'disabling'
  return <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose() }}>
    <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(640px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
      <DialogHeader>
        <DialogTitle>Add GitHub repository</DialogTitle>
        <DialogDescription>Choose a repository, then review the branch and revision to use for new runs.</DialogDescription>
      </DialogHeader>
      <div className="min-h-0 space-y-4 overflow-y-auto px-1">
        {!workspace && !uncertain && <GitHubConnection client={client} onConnected={() => { setConnected(true); void load() }} onDisconnected={() => {
          listGeneration.current += 1
          setConnected(false); setAccount(null); setRepositories([]); setSelected(null); setLoading(false)
        }} />}
        {connected && !workspace && <fieldset disabled={busy || uncertain} className="space-y-3">
          {account && <p className="text-ui-sm">Repositories accessible to <strong>{account.login}</strong>, including organizations and collaborators.</p>}
          <Label className="block space-y-1">Find a repository<Input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Filter loaded repositories" /></Label>
          {loading && <p role="status" className="text-ui-sm text-muted">Loading repositories…</p>}
          {!loading && account && repositories.length === 0 && <p>No accessible repositories were found for this account.</p>}
          {!loading && repositories.length > 0 && visible.length === 0 && <p>No loaded repositories match. Load more or change the filter.</p>}
          <div role="group" aria-label="Repositories" className="max-h-56 space-y-1 overflow-y-auto">
            {visible.map((repo) => <ListRow key={repo.id} selected={selected?.id === repo.id} aria-pressed={selected?.id === repo.id} title={repo.full_name} onClick={() => {
              setSelected(repo); setName(repo.name); setBranch(repo.default_branch); setOrigin(repo.can_push ? repo.clone_url : '')
            }}>{repo.full_name} · {repo.private ? 'Private' : 'Public'}</ListRow>)}
          </div>
          <div className="flex gap-2">
            {nextPage && <Button size="sm" variant="secondary" disabled={loading} onClick={() => { void load(nextPage) }}>Load more</Button>}
            <Button size="sm" variant="secondary" disabled={loading} onClick={() => { void load() }}>Reload repositories</Button>
          </div>
          {selected && <section aria-label="Selected repository" className="space-y-2 border-t pt-3 text-ui-sm">
            <p>Selected: <strong>{selected.full_name}</strong></p>
            <p>Workspace: {name} · Branch: {branch || 'No default branch available'}</p>
            <p>Publish changes to: <strong>{origin === selected.clone_url ? selected.full_name : origin || 'Not configured'}</strong></p>
            {!origin && <p className="text-muted">You can run agents with read-only source access. Choose a writable repository or fork under Advanced to publish their changes.</p>}
            {!selected.default_branch && <p role="status">This repository has no default branch. Add an initial commit on GitHub, then reload repositories, or specify an existing branch under Advanced.</p>}
            <details><summary className="cursor-pointer text-accent">Advanced</summary>
              <div className="mt-3 space-y-3">
                <Label className="block space-y-1">Workspace name<Input value={name} onChange={(event) => setName(event.target.value)} /></Label>
                <Label className="block space-y-1">Base branch<Input value={branch} onChange={(event) => setBranch(event.target.value)} /></Label>
                <Label className="block space-y-1">Publish destination (Origin)<select className="h-9 w-full rounded border bg-canvas px-2" value={origin} onChange={(event) => setOrigin(event.target.value)}>
                  <option value="">None — do not publish</option>
                  {repositories.filter((repo) => repo.can_push).map((repo) => <option key={repo.id} value={repo.clone_url}>{repo.full_name}</option>)}
                </select></Label>
                <p className="text-muted">The selected repository is suggested when you can write to it. Choose another writable repository or fork, or None to leave publishing unconfigured. Importing does not push code.</p>
              </div>
            </details>
          </section>}
        </fieldset>}
        <p className="text-ui-sm text-muted">Your GitHub connection is stored on this server. Runs you launch can use it. Imported code is shared with workspace members; your credentials are not copied to them.</p>
        {error && <p role="alert" className="whitespace-pre-wrap break-words text-ui-sm text-state-failed">{error}</p>}
        {error && !workspace && !uncertain && <p className="text-ui-sm">Check your GitHub connection and reload repositories if the account or access changed. For an empty repository, add an initial commit on GitHub before importing.</p>}
        {uncertain && <p role="alert" className="text-ui-sm">{workspace ? 'The last operation could not be confirmed. Check source status before taking another action; do not import again.' : 'The response did not establish whether a workspace was created. Close this dialog and inspect Workspaces before importing again; do not blindly repeat creation.'}</p>}
        {workspace && <section aria-label="Review repository revision" className="space-y-2 border-y py-3 text-ui">
          <p>Workspace: <strong>{workspace.name}</strong> · <code className="break-all text-ui-sm">{workspace.id}</code></p>
          <p className="break-all text-ui-sm">Source: {mirror?.source_url ?? selected?.full_name}</p>
          <p>Use <strong>{mirror?.branch ?? workspace.base_branch}</strong> for new runs.</p>
          <p className="break-all text-ui-sm">Publish changes to: {workspace.origin || 'Not configured'}</p>
          <p className="break-all text-ui-sm text-muted">Revision: <code>{mirror?.observed_commit || 'No revision fetched yet'}</code></p>
          {mirror?.accepted_commit && <p className="break-all text-ui-sm">Accepted revision: <code>{mirror.accepted_commit}</code></p>}
          <p className="text-ui-sm text-muted">Source status: {mirror?.status ?? 'Not configured'}</p>
          <p className="text-ui-sm">{canAdopt ? 'Review this exact revision before accepting it as the initial base. If the candidate changes, use Check source status to review the new revision before choosing Use repository again.' : 'The workspace is retained. A successful fetch and explicit acceptance are required before it is ready.'}</p>
          {mirror?.warning && <p role="status" className="text-ui-sm">{mirror.warning}</p>}
          {mirror?.status === 'auth-failed' && <p className="text-ui-sm">Open Source settings to reconnect GitHub using the authorizing account, then return here and retry the fetch. To change the bound account or source branch, explicitly save the new source settings for this workspace.</p>}
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" size="sm" disabled={busy} onClick={() => { void updateSource('status') }}>Check source status</Button>
            <Button variant="secondary" size="sm" disabled={busy || uncertain} onClick={() => { void updateSource('refresh') }}>Retry fetch</Button>
            <Button variant="secondary" size="sm" disabled={busy || uncertain} onClick={() => { reviewed.current = null; setSourceOpen(true) }}>Source settings</Button>
          </div>
        </section>}
      </div>
      <DialogFooter className="border-t pt-3">
        <Button variant="secondary" disabled={busy} onClick={onClose}>{workspace || uncertain ? 'Close' : 'Cancel'}</Button>
        {workspace ? <Button disabled={busy || uncertain || !canAdopt} onClick={() => { void updateSource('adopt') }}>{busy ? 'Working…' : 'Use repository'}</Button>
          : <Button disabled={busy || uncertain || !account || !selected || !name.trim() || !branch.trim()} onClick={() => { void importRepository() }}>{busy ? 'Fetching repository…' : 'Review repository'}</Button>}
      </DialogFooter>
    </DialogContent>
  </Dialog>
}
