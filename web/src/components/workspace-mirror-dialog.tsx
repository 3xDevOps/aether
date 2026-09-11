import { useEffect, useRef, useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { api, type Api } from '@/lib/api'
import type {
  WorkspaceMirrorAuth,
  WorkspaceMirrorResult,
} from '@/lib/types'
import { useStore } from '@/store'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'


function githubDeployKeyURL(source: string): string | null {
  const normalized = source.trim().replace(/\.git$/, '')
  let path = ''
  if (normalized.startsWith('https://github.com/')) {
    path = normalized.slice('https://github.com/'.length)
  } else {
    const match = normalized.match(/^(?:ssh:\/\/git@|git@)github\.com[/:](.+)$/)
    path = match?.[1] ?? ''
  }
  if (!path || path.split('/').length !== 2 || path.includes('?') || path.includes('#')) {
    return null
  }
  return `https://github.com/${path}/settings/keys`
}


export function WorkspaceMirrorDialog({
  workspaceID,
  client = api,
  suggestedSource,
  onStatusChange,
  onClose,
}: {
  workspaceID: string
  client?: Api
  /** A checkout remote to offer before the workspace's existing origin. */
  suggestedSource?: string
  onStatusChange?: (result: WorkspaceMirrorResult) => void
  onClose: () => void
}) {
  const workspace = useStore((s) => s.workspaces[workspaceID])
  const [result, setResult] = useState<WorkspaceMirrorResult | null>(null)
  const [source, setSource] = useState(suggestedSource ?? workspace?.origin ?? '')
  const [branch, setBranch] = useState(workspace?.base_branch ?? 'main')
  const [auth, setAuth] = useState<WorkspaceMirrorAuth>('public')
  const [knownHosts, setKnownHosts] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [disableStateUnavailable, setDisableStateUnavailable] = useState(false)
  const [confirmation, setConfirmation] = useState<'adopt' | 'disable' | null>(null)
  const publicKeyRef = useRef<HTMLPreElement>(null)


  useEffect(() => {
    let live = true
    void client.workspaceMirrorStatus(workspaceID).then(
      (current) => {
        if (!live) return
        setResult(current)
        setDisableStateUnavailable(false)
        onStatusChange?.(current)
        if (current.enabled) {
          setSource(current.source_url ?? '')
          setBranch(current.branch ?? workspace?.base_branch ?? 'main')
          setAuth(current.auth ?? 'public')
        }
        setLoading(false)
      },
      (err) => {
        if (!live) return
        setError(message(err))
        setLoading(false)
      },
    )
    return () => {
      live = false
    }
  }, [client, workspaceID, workspace?.base_branch, onStatusChange])

  const configure = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const current = await client.workspaceMirrorConfigure({
        workspace_id: workspaceID,
        source_url: source.trim(),
        branch: branch.trim(),
        auth,
        ...(auth === 'deploy-key' && knownHosts.trim()
          ? { known_hosts: knownHosts }
          : {}),
      })
      setResult(current)
      onStatusChange?.(current)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const refresh = async () => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const current = await client.workspaceMirrorRefresh(workspaceID)
      setResult(current)
      onStatusChange?.(current)
    } catch (err) {
      const refreshError = message(err)
      setError(refreshError)
      try {
        const current = await client.workspaceMirrorStatus(workspaceID)
        setResult(current)
        onStatusChange?.(current)
      } catch {
        // Keep the original refresh error when the persisted status is unavailable.
      }
    } finally {
      setBusy(false)
    }
  }

  const adopt = async () => {
    if (busy || !result?.generation) return
    setBusy(true)
    setError(null)
    try {
      const current = await client.workspaceMirrorAdopt(workspaceID, result.generation)
      setResult(current)
      onStatusChange?.(current)
      toast.success('Workspace source candidate adopted')
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const disable = async () => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const current = await client.workspaceMirrorDisable(workspaceID)
      setResult(current)
      setDisableStateUnavailable(false)
      onStatusChange?.(current)
      toast.success('Workspace source disabled')
    } catch (err) {
      setError(message(err))
      // Do not leave stale ready/local-only controls visible after a failed
      // disable. Only a successful status read can establish the next action.
      setResult(null)
      setDisableStateUnavailable(true)
      try {
        const current = await client.workspaceMirrorStatus(workspaceID)
        setResult(current)
        setDisableStateUnavailable(false)
        onStatusChange?.(current)
      } catch {
        // Keep the original disable error and withhold all stale actions.
      }
    } finally {
      setBusy(false)
    }
  }

  const isDisabling = result?.status === 'disabling'
  const candidate =
    result?.enabled &&
    result.observed_commit &&
    result.observed_commit !== result.accepted_commit
      ? result.observed_commit
      : null
  const githubURL = auth === 'deploy-key' ? githubDeployKeyURL(source) : null
  const configuredGithubURL =
    result?.auth === 'deploy-key' ? githubDeployKeyURL(result.source_url ?? source) : null

  return (
    <>
      <Dialog open onOpenChange={onClose}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(640px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
          <DialogHeader>
            <DialogTitle>Workspace Source</DialogTitle>
            <DialogDescription>
              {workspace?.name ?? workspaceID} · optional server-owned base source
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 space-y-4 overflow-y-auto -mx-1 px-1">
            {loading && <p className="text-xs text-muted-foreground">Loading source status…</p>}
            {error && (
              <p role="alert" className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-xs text-state-failed">
                {error}
              </p>
            )}
            {disableStateUnavailable && (
              <p role="status" className="text-xs text-muted-foreground">
                The persisted source state could not be confirmed after disable failed. Reopen the dialog before trying again.
              </p>
            )}
            {!loading && result && (
              <section aria-label="Source status" className="space-y-2 border-y bg-sidebar/40 px-3 py-2.5">
                <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
                  <p className="text-xs font-medium text-muted-foreground">State</p>
                  <p className="font-mono text-[13px]" data-testid="mirror-state">
                    {isDisabling ? 'disabling' : result.enabled ? result.status ?? 'Unknown' : 'local-only'}
                  </p>
                </div>
                {isDisabling && (
                  <p role="status" className="text-xs text-muted-foreground">
                    Disabling the workspace source; other actions are unavailable until cleanup finishes.
                  </p>
                )}
                {result.enabled && !isDisabling && (
                  <dl className="grid min-w-0 gap-x-4 gap-y-2 text-xs sm:grid-cols-2">
                    <div className="min-w-0">
                      <dt className="text-muted-foreground">Source</dt>
                      <dd className="mt-0.5 break-all font-mono" title={result.source_url}>
                        {result.source_url || '—'}
                      </dd>
                    </div>
                    <div className="min-w-0">
                      <dt className="text-muted-foreground">Source identity</dt>
                      <dd className="mt-0.5 break-all font-mono">{result.source_identity || '—'}</dd>
                    </div>
                    <div className="min-w-0">
                      <dt className="text-muted-foreground">Branch</dt>
                      <dd className="mt-0.5 break-all font-mono">{result.branch || '—'}</dd>
                    </div>
                    <div className="min-w-0">
                      <dt className="text-muted-foreground">Observed SHA</dt>
                      <dd className="mt-0.5 break-all font-mono">{result.observed_commit || '—'}</dd>
                    </div>
                    <div className="min-w-0">
                      <dt className="text-muted-foreground">Accepted SHA</dt>
                      <dd className="mt-0.5 break-all font-mono">{result.accepted_commit || '—'}</dd>
                    </div>
                    <div className="min-w-0 sm:col-span-2">
                      <dt className="text-muted-foreground">Last checked</dt>
                      <dd className="mt-0.5 break-all font-mono">
                        {result.last_attempt_at ?? result.updated_at ?? '—'}
                      </dd>
                    </div>
                  </dl>
                )}
                {!isDisabling && result.last_error && (
                  <p role="status" className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-xs text-state-failed">
                    {result.last_error}
                  </p>
                )}
                {!isDisabling && result.warning && (
                  <p role="status" className="border-l-2 border-state-attention/60 bg-state-attention/5 px-3 py-2 text-xs text-state-attention">
                    {result.warning}
                  </p>
                )}
                {!isDisabling && candidate && (
                  <div className="space-y-2 border-t border-border/70 pt-2">
                    <p className="text-xs text-state-attention">
                      Candidate SHA <span className="font-mono">{candidate}</span> is not accepted.
                    </p>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={busy}
                      onClick={() => setConfirmation('adopt')}
                    >
                      Adopt candidate
                    </Button>
                  </div>
                )}
              </section>
            )}
            {!loading && !disableStateUnavailable && !isDisabling && (
              <form id="workspace-mirror" className="space-y-3" onSubmit={(event) => void configure(event)}>
                <div className="space-y-1">
                  <Label htmlFor="workspace-mirror-source">Source URL</Label>
                  <Input
                    id="workspace-mirror-source"
                    value={source}
                    onChange={(event) => setSource(event.target.value)}
                    placeholder="https://github.com/org/repository.git"
                    autoComplete="off"
                    required
                  />
                  <p className="text-xs text-muted-foreground">
                    This source is fetched by the server and owns the workspace base branch.
                  </p>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="workspace-mirror-branch">Source branch</Label>
                  <Input
                    id="workspace-mirror-branch"
                    value={branch}
                    onChange={(event) => setBranch(event.target.value)}
                    required
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="workspace-mirror-auth">Authentication</Label>
                  <select
                    id="workspace-mirror-auth"
                    className="h-[26px] min-h-[26px] w-full rounded-[2px] border border-input bg-background px-2 text-[13px]"
                    value={auth}
                    onChange={(event) => setAuth(event.target.value as WorkspaceMirrorAuth)}
                  >
                    <option value="public">Public HTTPS</option>
                    <option value="deploy-key">Deploy key</option>
                  </select>
                </div>
                {auth === 'deploy-key' && (
                  <div className="space-y-2 border-l-2 border-border/70 pl-3">
                    <div className="space-y-1">
                      <Label htmlFor="workspace-mirror-known-hosts">known_hosts (generic SSH only)</Label>
                      <Textarea
                        id="workspace-mirror-known-hosts"
                        value={knownHosts}
                        onChange={(event) => setKnownHosts(event.target.value)}
                        placeholder="git.example.com ssh-ed25519 AAAA…"
                        rows={3}
                      />
                      <p className="text-xs text-muted-foreground">
                        Paste the exact host key for a non-GitHub SSH source. GitHub uses its pinned host key.
                      </p>
                    </div>
                    {(result?.public_key || (result?.auth === 'deploy-key' && result.enabled)) && (
                      <div className="space-y-1">
                        <Label htmlFor="workspace-mirror-public-key">Public deploy key</Label>
                        <div className="flex min-w-0 gap-2">
                          <pre
                            id="workspace-mirror-public-key"
                            ref={publicKeyRef}
                            className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all border border-border/70 bg-muted px-2 py-1.5 font-mono text-xs"
                          >
                            {result?.public_key || 'The public key is only shown by the server after key setup.'}
                          </pre>
                          {result?.public_key && (
                            <Button
                              type="button"
                              size="sm"
                              variant="outline"
                              onClick={() => void copyText(result.public_key ?? '', publicKeyRef.current)}
                            >
                              Copy public key
                            </Button>
                          )}
                        </div>
                        {(githubURL || configuredGithubURL) && (
                          <div className="space-y-1 text-xs">
                            <a
                              className="text-primary underline underline-offset-2"
                              href={githubURL || configuredGithubURL || '#'}
                              target="_blank"
                              rel="noreferrer"
                            >
                              Install this key in GitHub deploy keys
                            </a>
                            <p className="break-all font-mono text-muted-foreground">
                              {githubURL || configuredGithubURL}
                            </p>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )}
              </form>
            )}
          </div>
          <DialogFooter className="flex-wrap border-t pt-3">
            {!disableStateUnavailable && (result?.enabled || isDisabling) && (
              <Button type="button" variant="destructive" onClick={() => setConfirmation('disable')} disabled={busy}>
                {isDisabling ? 'Retry disable' : 'Disable source'}
              </Button>
            )}
            <span className="flex-1" />
            <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
              Close
            </Button>
            {!disableStateUnavailable && !isDisabling && result?.enabled && (
              <Button type="button" variant="outline" onClick={() => void refresh()} disabled={busy}>
                {busy ? 'Checking…' : result.status === 'ready' ? 'Refresh' : 'Verify'}
              </Button>
            )}
            {!disableStateUnavailable && !isDisabling && (
              <Button type="submit" form="workspace-mirror" disabled={busy || loading}>
                {busy ? 'Saving…' : result?.enabled ? 'Save source' : 'Configure source'}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <AlertDialog open={confirmation === 'adopt'} onOpenChange={(open) => !open && setConfirmation(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Adopt source candidate?</AlertDialogTitle>
            <AlertDialogDescription>
              This explicitly replaces the accepted workspace base with candidate SHA{' '}
              <span className="font-mono">{candidate}</span>. Review the source change before continuing.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void adopt()} disabled={busy}>
              Adopt candidate
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={confirmation === 'disable'} onOpenChange={(open) => !open && setConfirmation(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Disable workspace source?</AlertDialogTitle>
            <AlertDialogDescription>
              The workspace becomes local-only and its base branch can be pushed by clients again. Existing deploy keys are not revoked on GitHub automatically.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void disable()} disabled={busy}>
              Disable source
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
