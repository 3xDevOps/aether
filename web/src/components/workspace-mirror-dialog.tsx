import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { copyText } from '@/lib/clipboard'
import { message } from '@/lib/format'
import { api, type Api } from '@/lib/api'
import type {
  WorkspaceMirrorAuth,
  WorkspaceMirrorResult,
} from '@/lib/types'
import { useStore } from '@/store'
import { useIsAdmin } from '@/store/hooks'
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
import { GitHubConnection } from '@/components/github-connection'
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
  const admin = useIsAdmin()
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
  const generation = useRef(0)
  const [contextVersion, setContextVersion] = useState(0)
  const previousContext = useRef({ client, workspaceID })

  useLayoutEffect(() => {
    const reset = () => {
      generation.current += 1
      setContextVersion((version) => version + 1)
      setResult(null); setSource(''); setBranch(''); setAuth('public'); setKnownHosts('')
      setBusy(false); setLoading(true); setError(null); setConfirmation(null)
      setDisableStateUnavailable(false)
    }
    if (previousContext.current.client !== client || previousContext.current.workspaceID !== workspaceID) {
      previousContext.current = { client, workspaceID }
      reset()
    }
    const unsubscribe = useStore.subscribe((state, previous) => {
      if (state.identityKey !== previous.identityKey || state.connectionEpoch !== previous.connectionEpoch || state.info?.member.id !== previous.info?.member.id || state.info?.member.role !== previous.info?.member.role) reset()
    })
    return () => { generation.current += 1; unsubscribe() }
  }, [client, workspaceID])

  const version = generation.current
  const isCurrent = () => version === generation.current


  useEffect(() => {
    let live = true
    const requestVersion = generation.current
    void client.workspaceMirrorStatus(workspaceID).then(
      (current) => {
        if (!live || requestVersion !== generation.current) return
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
        if (!live || requestVersion !== generation.current) return
        setError(message(err))
        setLoading(false)
      },
    )
    return () => {
      live = false
    }
  }, [client, workspaceID, workspace?.base_branch, onStatusChange, contextVersion])

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
      if (!isCurrent()) return
      setResult(current)
      onStatusChange?.(current)
    } catch (err) {
      if (!isCurrent()) return
      setError(message(err))
    } finally {
      if (isCurrent()) setBusy(false)
    }
  }

  const refresh = async () => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const current = await client.workspaceMirrorRefresh(workspaceID)
      if (!isCurrent()) return
      setResult(current)
      onStatusChange?.(current)
    } catch (err) {
      if (!isCurrent()) return
      const refreshError = message(err)
      setError(refreshError)
      try {
        const current = await client.workspaceMirrorStatus(workspaceID)
        if (!isCurrent()) return
        setResult(current)
        onStatusChange?.(current)
      } catch {
        // Keep the original refresh error when the persisted status is unavailable.
      }
    } finally {
      if (isCurrent()) setBusy(false)
    }
  }

  const adopt = async () => {
    if (busy || !result?.generation) return
    setBusy(true)
    setError(null)
    try {
      const current = await client.workspaceMirrorAdopt(workspaceID, result.generation)
      if (!isCurrent()) return
      setResult(current)
      onStatusChange?.(current)
      toast.success('Workspace source candidate adopted')
    } catch (err) {
      if (!isCurrent()) return
      setError(message(err))
    } finally {
      if (isCurrent()) setBusy(false)
    }
  }

  const disable = async () => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const current = await client.workspaceMirrorDisable(workspaceID)
      if (!isCurrent()) return
      setResult(current)
      setDisableStateUnavailable(false)
      onStatusChange?.(current)
      toast.success('Workspace source disabled')
    } catch (err) {
      if (!isCurrent()) return
      setError(message(err))
      // Only a successful status read can establish the next action.
      setResult(null)
      setDisableStateUnavailable(true)
      try {
        const current = await client.workspaceMirrorStatus(workspaceID)
        if (!isCurrent()) return
        setResult(current)
        setDisableStateUnavailable(false)
        onStatusChange?.(current)
      } catch {
        // Keep the original disable error and withhold all stale actions.
      }
    } finally {
      if (isCurrent()) setBusy(false)
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
      <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose() }}>
        <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-[min(640px,calc(100%-2rem))] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden">
          <DialogHeader>
            <DialogTitle>Workspace Source</DialogTitle>
            <DialogDescription>
              {workspace?.name ?? workspaceID} · optional server-owned base source
            </DialogDescription>
          </DialogHeader>
          <div className="min-h-0 space-y-4 overflow-y-auto -mx-1 px-1">
            {loading && <p className="text-ui-sm text-muted">Loading source status…</p>}
            {error && (
              <p role="alert" className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-ui-sm text-state-failed">
                {error}
              </p>
            )}
            {disableStateUnavailable && (
              <p role="status" className="text-ui-sm text-muted">
                The persisted source state could not be confirmed after disable failed. Reopen the dialog before trying again.
              </p>
            )}
            {!loading && result && (
              <section aria-label="Source status" className="space-y-2 border-y bg-chrome/40 px-3 py-2.5">
                <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
                  <p className="text-ui-sm font-medium text-muted">State</p>
                  <p className="font-code text-ui" data-testid="mirror-state">
                    {isDisabling ? 'disabling' : result.enabled ? result.status ?? 'Unknown' : 'local-only'}
                  </p>
                </div>
                {isDisabling && (
                  <p role="status" className="text-ui-sm text-muted">
                    Disabling the workspace source; other actions are unavailable until cleanup finishes.
                  </p>
                )}
                {result.enabled && !isDisabling && (
                  <dl className="grid min-w-0 gap-x-4 gap-y-2 text-ui-sm sm:grid-cols-2">
                    <div className="min-w-0">
                      <dt className="text-muted">Source</dt>
                      <dd className="mt-0.5 break-all font-code" title={result.source_url}>
                        {result.source_url || '-'}
                      </dd>
                    </div>
                    <div className="min-w-0">
                      <dt className="text-muted">Source identity</dt>
                      <dd className="mt-0.5 break-all font-code">{result.source_identity || '-'}</dd>
                    </div>
                    <div className="min-w-0">
                      <dt className="text-muted">Branch</dt>
                      <dd className="mt-0.5 break-all font-code">{result.branch || '-'}</dd>
                    </div>
                    {result.auth === 'github' && <div className="min-w-0 sm:col-span-2">
                      <dt className="text-muted">GitHub source authorization</dt>
                      <dd className="mt-0.5 break-all">Account ID {result.github_user_id ?? 'unavailable'} · authorizing member {result.github_member_id ?? 'unavailable'}</dd>
                    </div>}
                    <div className="min-w-0">
                      <dt className="text-muted">Observed SHA</dt>
                      <dd className="mt-0.5 break-all font-code">{result.observed_commit || '-'}</dd>
                    </div>
                    <div className="min-w-0">
                      <dt className="text-muted">Accepted SHA</dt>
                      <dd className="mt-0.5 break-all font-code">{result.accepted_commit || '-'}</dd>
                    </div>
                    <div className="min-w-0 sm:col-span-2">
                      <dt className="text-muted">Last checked</dt>
                      <dd className="mt-0.5 break-all font-code">
                        {result.last_attempt_at ?? result.updated_at ?? '-'}
                      </dd>
                    </div>
                  </dl>
                )}
                {!isDisabling && result.last_error && (
                  <p role="status" className="border-l-2 border-state-failed/60 bg-state-failed/5 px-3 py-2 text-ui-sm text-state-failed">
                    {result.last_error}
                  </p>
                )}
                {!isDisabling && result.warning && (
                  <p role="status" className="border-l-2 border-state-needs-you/60 bg-state-needs-you-soft px-3 py-2 text-ui-sm text-state-needs-you">
                    {result.warning}
                  </p>
                )}
                {!isDisabling && candidate && (
                  <div className="space-y-2 border-t border-seam/70 pt-2">
                    <p className="text-ui-sm text-state-needs-you">
                      Candidate SHA <span className="font-code">{candidate}</span> is not accepted.
                    </p>
                    <Button
                      type="button"
                      size="sm"
                      variant="secondary"
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
                  <p className="text-ui-sm text-muted">
                    Use a credential-free URL. This source is fetched by the server and owns the workspace base branch; it does not set checkout Origin or grant publishing access.
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
                    className="h-[26px] min-h-[26px] w-full rounded-[2px] border border-control bg-canvas px-2 text-ui"
                    value={auth}
                    onChange={(event) => setAuth(event.target.value as WorkspaceMirrorAuth)}
                  >
                    <option value="public">Public HTTPS</option>
                    <option value="deploy-key">Private repository - read-only deploy key</option>
                    {admin && <option value="github">Connected GitHub account</option>}
                  </select>
                </div>
                {auth === 'github' && <div className="space-y-2 border-l-2 border-seam/70 pl-3">
                  <GitHubConnection client={client} />
                  <p className="text-ui-sm text-muted">The server fetches this GitHub source using the authorizing member's native account. Save source explicitly binds it to your current connected account. Existing accepted code remains available if authorization fails; reconnect the original account or save to rebind, then refresh and review the fetched revision. Credentials are not copied to workspace members.</p>
                </div>}
                {auth === 'deploy-key' && (
                  <div className="space-y-2 border-l-2 border-seam/70 pl-3">
                    <div className="space-y-1">
                      <Label htmlFor="workspace-mirror-known-hosts">known_hosts (generic SSH only)</Label>
                      <Textarea
                        id="workspace-mirror-known-hosts"
                        value={knownHosts}
                        onChange={(event) => setKnownHosts(event.target.value)}
                        placeholder="git.example.com ssh-ed25519 AAAA…"
                        rows={3}
                      />
                      <p className="text-ui-sm text-muted">
                        Paste the exact host key verified with the host administrator for a non-GitHub SSH source. Do not blindly trust ssh-keyscan output. GitHub uses its pinned host key.
                      </p>
                    </div>
                    <p className="text-ui-sm text-muted">Install the generated key read-only with a repository administrator. It is not your native Git/gh publishing credential. After installation use Verify / Refresh; Save source rotates the key. Fetching is not approval: explicitly adopt the reviewed candidate.</p>
                    {(result?.public_key || (result?.auth === 'deploy-key' && result.enabled)) && (
                      <div className="space-y-1">
                        <Label htmlFor="workspace-mirror-public-key">Public deploy key</Label>
                        <div className="flex min-w-0 gap-2">
                          <pre
                            id="workspace-mirror-public-key"
                            ref={publicKeyRef}
                            className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all border border-seam/70 bg-chrome px-2 py-1.5 font-code text-ui-sm"
                          >
                            {result?.public_key || 'The public key is only shown by the server after key setup.'}
                          </pre>
                          {result?.public_key && (
                            <Button
                              type="button"
                              size="sm"
                              variant="secondary"
                              onClick={() => void copyText(result.public_key ?? '', publicKeyRef.current)}
                            >
                              Copy public key
                            </Button>
                          )}
                        </div>
                        {(githubURL || configuredGithubURL) && (
                          <div className="space-y-1 text-ui-sm">
                            <a
                              className="text-accent underline underline-offset-2"
                              href={githubURL || configuredGithubURL || '#'}
                              target="_blank"
                              rel="noreferrer"
                            >
                              Install this key in GitHub deploy keys
                            </a>
                            <p className="break-all font-code text-muted">
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
              <Button type="button" variant="danger" onClick={() => setConfirmation('disable')} disabled={busy}>
                {isDisabling ? 'Retry disable' : 'Disable source'}
              </Button>
            )}
            <span className="flex-1" />
            <Button type="button" variant="secondary" onClick={onClose} disabled={busy}>
              Close
            </Button>
            {!disableStateUnavailable && !isDisabling && result?.enabled && (
              <Button type="button" variant="secondary" onClick={() => void refresh()} disabled={busy}>
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
              <span className="font-code">{candidate}</span>. Review the source change before continuing.
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
