import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type {
  FileRead, Run, RunGitCommitResult, RunGitDiffResult, RunGitExpected, RunGitPushResult,
  RunGitPushTarget, RunGitStatusResult, RunPRCreateResult, RunPRFeedbackResult,
  RunPRStatusResult, RunPRTarget, RunRepoCommandOutput,
} from '@/lib/types'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { parsePatch } from './parse'
import { FilePatch } from './patch-view'

interface PRReview {
  target: RunPRTarget
  expected: RunGitExpected
  result: RunPRStatusResult
}

export function NativeChanges({ run, wrap, client = api }: { run: Run; wrap: boolean; client?: Api }) {
  const caps = useCapability()
  const self = useSelf()
  const [status, setStatus] = useState<RunGitStatusResult | null>(null)
  const [statusReady, setStatusReady] = useState(false)
  const [untracked, setUntracked] = useState<{ path: string; file?: FileRead; error?: string }[]>([])
  const [paths, setPaths] = useState<string[]>([])
  const [patches, setPatches] = useState<RunGitDiffResult[] | null>(null)
  const [reviewed, setReviewed] = useState<RunGitExpected | null>(null)
  const [commitMessage, setCommitMessage] = useState('')
  const [commit, setCommit] = useState<RunGitCommitResult | null>(null)
  const [push, setPush] = useState<RunGitPushResult | null>(null)
  const [pushTarget, setPushTarget] = useState<RunGitPushTarget>({ remote: '', repository: '', head_branch: '' })
  const [pushReviewed, setPushReviewed] = useState(false)
  const [target, setTarget] = useState<RunPRTarget>({ repository: '', base_branch: '', head_repository: '', head_branch: '' })
  const [prReview, setPRReview] = useState<PRReview | null>(null)
  const [prReviewed, setPRReviewed] = useState(false)
  const [creation, setCreation] = useState<RunPRCreateResult | null>(null)
  const [uncertain, setUncertain] = useState(false)
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [draft, setDraft] = useState(false)
  const [feedback, setFeedback] = useState<RunPRFeedbackResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const mounted = useRef(false)
  const operation = useRef(false)
  const canWrite = self.role === 'admin' || self.role === 'collaborator'
  const expected = statusReady && status && !status.error ? { branch: status.branch, head: status.head } : null
  const targetComplete = Object.values(target).every((value) => value.trim() !== '')
  const currentPRReview = prReview && expected && prReview.expected.branch === expected.branch && prReview.expected.head === expected.head &&
    JSON.stringify(prReview.target) === JSON.stringify(target) ? prReview : null
  const pr = currentPRReview?.result.pull_request

  async function refreshStatus() {
    setStatusReady(false)
    setReviewed(null)
    setPushReviewed(false)
    setPRReviewed(false)
    setPRReview(null)
    const next = await client.runGitStatus({ run_id: run.id })
    if (!mounted.current) return
    setStatus(next)
    setStatusReady(true)
    setPaths((selected) => selected.filter((path) => (next.changes ?? []).some((change) => change.path === path)))
    setPatches(null)
    setUntracked([])
  }

  useEffect(() => {
    let live = true
    mounted.current = true
    operation.current = true
    setBusy(true)
    void client.runGitStatus({ run_id: run.id }).then(
      (next) => { if (live) { setStatus(next); setStatusReady(true) } },
      (cause: unknown) => { if (live) setError(message(cause)) },
    ).finally(() => { if (live) { operation.current = false; setBusy(false) } })
    return () => { live = false; mounted.current = false }
  }, [client, run.id])

  async function perform(action: () => Promise<void>) {
    if (operation.current) return
    operation.current = true
    setBusy(true)
    setError(null)
    try {
      await action()
    } catch (cause) {
      if (mounted.current) setError(message(cause))
    } finally {
      operation.current = false
      if (mounted.current) setBusy(false)
    }
  }

  function selectPath(path: string) {
    setPaths((selected) => selected.includes(path) ? selected.filter((entry) => entry !== path) : [...selected, path])
    setPatches(null)
    setUntracked([])
    setReviewed(null)
  }

  async function reviewPaths() {
    if (!paths.length || !expected) return
    setReviewed(null)
    const diffPaths = [...new Set(paths.flatMap((path) => {
      const change = status?.changes?.find((entry) => entry.path === path)
      return change?.original_path && (change.index === 'R' || change.worktree === 'R')
        ? [path, change.original_path]
        : [path]
    }))]
    const [results, previews] = await Promise.all([
      Promise.all([
        client.runGitDiff({ run_id: run.id, paths: diffPaths, staged: false }),
        client.runGitDiff({ run_id: run.id, paths: diffPaths, staged: true }),
      ]),
      Promise.all((status?.changes ?? []).filter((change) => change.untracked && paths.includes(change.path)).map(async (change) => {
        try {
          return { path: change.path, file: await client.filesRead({ workspace_id: run.workspace_id, run_id: run.id, path: change.path }) }
        } catch (cause) {
          return { path: change.path, error: message(cause) }
        }
      })),
    ])
    if (!mounted.current) return
    setPatches(results)
    setUntracked(previews)
    if (results.some((result) => result.error || result.output.truncated) || previews.some((preview) => preview.error || preview.file?.truncated)) return
    if (!results.every((result) => result.state.branch === expected.branch && result.state.head === expected.head)) {
      setError('Branch or HEAD changed during review. Refresh native status and review the selected paths again.')
      return
    }
    setReviewed(expected)
  }

  async function commitPaths() {
    if (!reviewed) return
    setReviewed(null)
    setStatusReady(false)
    const result = await client.runGitCommit({ run_id: run.id, expected: reviewed, paths, message: commitMessage })
    setCommit(result)
    if (result.committed) setCommitMessage('')
    await refreshStatus()
    useStore.getState().refreshDiff(run.id)
  }

  async function publishBranch() {
    if (!expected || !pushReviewed) return
    setPushReviewed(false)
    setStatusReady(false)
    const result = await client.runGitPush({ run_id: run.id, expected, target: pushTarget })
    setPush(result)
    await refreshStatus()
  }

  async function discoverPR() {
    if (!expected) return
    setPRReviewed(false)
    setPRReview(null)
    const result = await client.runPRStatus({ run_id: run.id, expected, target })
    if (!mounted.current) return
    setPRReview({ target: { ...target }, expected, result })
    if (result.pull_request && !result.error) setUncertain(false)
  }

  async function createPR() {
    if (!currentPRReview || !prReviewed || uncertain) return
    setPRReviewed(false)
    // A lost response can follow a successful GitHub mutation. Only an exact
    // read-only discovery is offered until that PR has been found.
    setUncertain(true)
    const result = await client.runPRCreate({
      run_id: run.id, expected: currentPRReview.expected, target: currentPRReview.target,
      expected_login: currentPRReview.result.identity, title, body, draft,
    })
    setCreation(result)
    setUncertain(result.creation_uncertain)
    if (result.pull_request) {
      setPRReview({ ...currentPRReview, result: {
        identity: result.identity, account_member_id: result.account_member_id,
        pull_request: result.pull_request, output: result.output, error: result.error, actual: result.actual,
      } })
    }
  }

  function changeTarget(key: keyof RunPRTarget, value: string) {
    setTarget((current) => ({ ...current, [key]: value }))
    setPRReview(null)
    setPRReviewed(false)
    setFeedback(null)
    setCreation(null)
  }

  return (
    <details className="shrink-0 border-b bg-sidebar" aria-label="Native changes and publishing">
      <summary className="cursor-pointer px-3 py-3 text-[13px] font-semibold">Native changes &amp; publish</summary>
      <div className="max-h-[70dvh] space-y-4 overflow-y-auto px-3 pb-4 text-[13px]">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="font-semibold">Run checkout</h2>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void perform(refreshStatus)}>Refresh native status</Button>
        </div>
        {error && <p role="alert" className="break-words text-state-failed">{error}</p>}
        {!status && !error && <p>Loading native Git status...</p>}
        {status && <>
          <dl className="grid gap-2 break-all sm:grid-cols-2">
            <div><dt className="text-muted-foreground">Branch / HEAD</dt><dd className="font-mono">{status.branch || '(detached)'} / {status.head || '(unborn)'}</dd></div>
            <div><dt className="text-muted-foreground">Agent account</dt><dd>{status.account_name || status.account_member_id} <code>{status.account_member_id}</code></dd></div>
            <div><dt className="text-muted-foreground">GitHub identity</dt><dd>{status.identity || 'Unavailable'}</dd></div>
            <div><dt className="text-muted-foreground">Checkout upstream</dt><dd>{status.upstream ? `${status.upstream.remote}/${status.upstream.branch}` : 'None'}</dd></div>
          </dl>
          {!statusReady && !busy && <p role="status">Showing the last known checkout state. Refresh native status before another mutation; a lost response does not establish whether an earlier action succeeded.</p>}
          {status.identity_error && <p role="alert" className="text-state-failed">{status.identity_error}</p>}
          <Diagnostics output={status.output} error={status.error} />
          {status.truncated && <p role="alert">Native status is incomplete. Only the exact paths returned below can be selected.</p>}
          <fieldset disabled={busy || !!status.error} className="min-w-0 space-y-2">
            <legend className="mb-2 font-semibold">Changed, staged and untracked paths</legend>
            {(status.changes ?? []).map((change) => <label key={change.path} className="flex items-start gap-2 break-all border-b py-2 font-mono">
              <input type="checkbox" className="mt-1" checked={paths.includes(change.path)} onChange={() => selectPath(change.path)} aria-label={`Select ${change.path}`} />
              <span className="min-w-0"><span className="whitespace-pre-wrap">{change.path}</span>{change.original_path && <span> (from {change.original_path})</span>}<span className="block text-xs text-muted-foreground">{change.untracked ? 'Untracked' : `Staged / index: ${change.index || ' '} · Worktree: ${change.worktree || ' '}`}{change.conflicted && ' · Conflicted'}</span></span>
            </label>)}
            {!status.changes?.length && <p>No native checkout changes.</p>}
          </fieldset>
          <Button size="sm" variant="outline" disabled={busy || !paths.length || !expected} onClick={() => void perform(reviewPaths)}>Review selected paths</Button>
          {patches?.map((result, index) => <section key={index} aria-label={index ? 'Selected staged diff' : 'Selected worktree diff'} className="min-w-0 overflow-hidden border">
            <h3 className="p-2 font-medium">{index ? 'Staged diff' : 'Worktree diff (tracked paths)'}</h3>
            <Diagnostics output={{ ...result.output, stdout: undefined }} error={result.error} />
            {parsePatch(result.output.stdout ?? '').map((file) => <FilePatch key={file.path} file={file} wrap={wrap} />)}
            {!result.output.stdout && !result.error && <p className="p-2 text-muted-foreground">No textual changes in this comparison.</p>}
          </section>)}
          {untracked.map((preview) => <section key={preview.path} aria-label={`Untracked contents: ${preview.path}`} className="space-y-1 border p-2">
            <h3 className="break-all font-medium">Untracked: {preview.path}</h3>
            {preview.error && <p role="alert" className="text-state-failed">{preview.error}</p>}
            {preview.file?.binary ? <p>Binary file; no textual preview.</p> : <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{preview.file?.content}</pre>}
            {preview.file?.truncated && <p role="alert">Untracked preview is truncated. Review this file in the run before committing with native Git.</p>}
          </section>)}
          <Label className="block space-y-1">Commit message<Textarea value={commitMessage} onChange={(event) => setCommitMessage(event.target.value)} disabled={busy} /></Label>
          <p className="text-xs text-muted-foreground">Commits use the selected paths' current worktree contents, not just their staged hunks. Other staged paths stay staged. Native identity/signing apply; commit hooks do not run.</p>
          <Button size="sm" disabled={busy || !canWrite || !caps.hasMethod('run.git.commit') || !reviewed || !commitMessage.trim()} onClick={() => void perform(commitPaths)}>Commit selected paths</Button>
          {commit && <section aria-label="Commit outcome" className="space-y-1 border p-2">
            <p>Committed: {commit.committed ? 'yes' : 'no'} · Index updated: {commit.index_updated ? 'yes' : 'no'} · Hooks run: {commit.hooks_run ? 'yes' : 'no'}</p>
            {commit.head && <p className="break-all font-mono">Commit HEAD: {commit.head}</p>}
            {commit.committed && !commit.index_updated && <p role="alert">The commit exists, but the index was not fully updated. Inspect native status; do not repeat this commit.</p>}
            <Actual actual={commit.actual} /><Diagnostics output={commit.output} error={commit.error} />
          </section>}
          <section aria-label="Publish branch" className="space-y-3 border-t pt-3">
            <h2 className="font-semibold">Push branch</h2>
            <p className="text-xs text-muted-foreground">Select a configured remote's exact writable push URL. This never changes checkout Origin, the mirror source or its accepted base. Configure additional remotes using native Git in the run terminal.</p>
            <fieldset disabled={busy} className="grid gap-3 sm:grid-cols-3">
              <Label className="space-y-1">Push remote<select className="h-9 w-full rounded border bg-background px-2" value={pushTarget.remote} onChange={(event) => { setPushTarget({ ...pushTarget, remote: event.target.value, repository: '' }); setPushReviewed(false) }}><option value="">Choose remote</option>{status.remotes?.map((remote) => <option key={remote.name} value={remote.name}>{remote.name}</option>)}</select></Label>
              <Label className="space-y-1">Writable push URL<select className="h-9 w-full rounded border bg-background px-2" value={pushTarget.repository} onChange={(event) => { setPushTarget({ ...pushTarget, repository: event.target.value }); setPushReviewed(false) }}><option value="">Choose exact URL</option>{status.remotes?.find((remote) => remote.name === pushTarget.remote)?.push_urls?.map((url) => <option key={url} value={url}>{url}</option>)}</select></Label>
              <Label className="space-y-1">Push head branch<Input value={pushTarget.head_branch} onChange={(event) => { setPushTarget({ ...pushTarget, head_branch: event.target.value }); setPushReviewed(false) }} /></Label>
            </fieldset>
            <label className="flex items-start gap-2"><input type="checkbox" disabled={busy || !expected} checked={pushReviewed} onChange={(event) => setPushReviewed(event.target.checked)} /><span>I reviewed the run account, branch, HEAD and exact push destination above.</span></label>
            <Button size="sm" disabled={busy || !canWrite || !caps.hasMethod('run.git.push') || !expected || !pushReviewed || !Object.values(pushTarget).every((value) => value.trim())} onClick={() => void perform(publishBranch)}>Push reviewed branch</Button>
            {push && <section aria-label="Push outcome" className="space-y-1 border p-2">
              <p>Pushed: {push.pushed ? 'yes' : 'no'}</p><p className="break-all font-mono">{push.head} → {push.target.repository} · {push.target.head_branch}</p>
              <Actual actual={push.actual} /><Diagnostics output={push.output} error={push.error} />
              {push.pushed && <p>A later PR error does not undo this push.</p>}
            </section>}
          </section>
          <section aria-label="GitHub pull request" className="space-y-3 border-t pt-3">
            <h2 className="font-semibold">GitHub pull request</h2>
            <p className="text-xs text-muted-foreground">PR repository/base and fork head are independent of the push remote. Enter owner/name, not a clone URL. Discover the exact existing PR before creating one. No merge, force push or branch switch is performed here.</p>
            <fieldset disabled={busy || uncertain} className="grid gap-3 sm:grid-cols-2">
              <Label className="space-y-1">PR repository (owner/name)<Input value={target.repository} onChange={(event) => changeTarget('repository', event.target.value)} /></Label>
              <Label className="space-y-1">PR base branch<Input value={target.base_branch} onChange={(event) => changeTarget('base_branch', event.target.value)} /></Label>
              <Label className="space-y-1">PR head repository (owner/name)<Input value={target.head_repository} onChange={(event) => changeTarget('head_repository', event.target.value)} /></Label>
              <Label className="space-y-1">PR head branch<Input value={target.head_branch} onChange={(event) => changeTarget('head_branch', event.target.value)} /></Label>
            </fieldset>
            <Button size="sm" variant="outline" disabled={busy || !expected || !targetComplete || !caps.hasMethod('run.pr.status')} onClick={() => void perform(discoverPR)}>{uncertain ? 'Reconcile PR read-only' : 'Discover existing PR'}</Button>
            {uncertain && <p role="alert">Creation outcome is uncertain. Keep this exact target and reconcile read-only; creation will not be retried.</p>}
            {currentPRReview && <>
              <p className="break-all">Reviewed GitHub identity: <strong>{currentPRReview.result.identity || 'Unavailable'}</strong> · Agent account: {currentPRReview.result.account_member_id}</p>
              <Actual actual={currentPRReview.result.actual} /><Diagnostics output={currentPRReview.result.output} error={currentPRReview.result.error} />
              {pr ? <div className="space-y-1 break-all"><a className="underline" href={pr.url} target="_blank" rel="noreferrer">#{pr.number}: {pr.title}</a><p>{pr.state}{pr.draft ? ' · Draft' : ''} · {pr.head_repository}:{pr.head_branch} → {pr.repository}:{pr.base_branch}</p><p className="font-mono">PR head: {pr.head_oid}</p></div> : !currentPRReview.result.error && <p>No existing PR for this exact repository, base and head.</p>}
            </>}
            {!pr && <>
              <Label className="block space-y-1">PR title<Input value={title} disabled={busy} onChange={(event) => setTitle(event.target.value)} /></Label>
              <Label className="block space-y-1">PR description<Textarea value={body} disabled={busy} onChange={(event) => setBody(event.target.value)} /></Label>
              <label className="flex items-center gap-2"><input type="checkbox" checked={draft} disabled={busy} onChange={(event) => setDraft(event.target.checked)} />Draft PR</label>
              <label className="flex items-start gap-2"><input type="checkbox" checked={prReviewed} disabled={busy || !currentPRReview || !!currentPRReview.result.error || !currentPRReview.result.identity} onChange={(event) => setPRReviewed(event.target.checked)} /><span>I reviewed this GitHub identity and the exact PR repository, base, fork and head.</span></label>
              <Button size="sm" disabled={busy || !canWrite || !caps.hasMethod('run.pr.create') || uncertain || !currentPRReview || !!currentPRReview.result.error || !prReviewed || !title.trim()} onClick={() => void perform(createPR)}>Create reviewed PR</Button>
            </>}
            {creation && <section aria-label="PR creation outcome" className="space-y-1 border p-2"><p>Created: {creation.created ? 'yes' : 'no'} · Reconciled: {creation.reconciled ? 'yes' : 'no'}</p><Actual actual={creation.actual} /><Diagnostics output={creation.output} error={creation.error} /></section>}
            <Button size="sm" variant="outline" disabled={busy || !expected || !targetComplete || !caps.hasMethod('run.pr.feedback')} onClick={() => void perform(async () => { setFeedback(null); if (expected) setFeedback(await client.runPRFeedback({ run_id: run.id, expected, target, limit: 100 })) })}>Refresh PR feedback</Button>
            {feedback && <PRFeedback key={JSON.stringify(target)} run={run} feedback={feedback} client={client} />}
          </section>
        </>}
      </div>
    </details>
  )
}

function Actual({ actual }: { actual?: RunGitExpected }) {
  return actual && <p role="alert" className="break-all">Actual branch / HEAD: <code>{actual.branch} / {actual.head}</code>. Refresh and review before another action.</p>
}

function Diagnostics({ output, error }: { output: RunRepoCommandOutput; error?: string }) {
  return <>
    {error && <p role="alert" className="whitespace-pre-wrap break-words text-state-failed">{error}</p>}
    {(output.stdout || output.stderr || output.exit_code !== 0 || output.truncated) && <details className="min-w-0" open={!!error || output.exit_code !== 0}>
      <summary className="cursor-pointer text-xs">Native diagnostics · exit {output.exit_code}{output.truncated ? ' · truncated' : ''}</summary>
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all p-2 text-xs">{output.stdout}{output.stderr}</pre>
    </details>}
  </>
}

interface FeedbackEntry { key: string; heading: string; body: string; url?: string }

function PRFeedback({ run, feedback, client }: { run: Run; feedback: RunPRFeedbackResult; client: Api }) {
  const [selected, setSelected] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [receipt, setReceipt] = useState<string | null>(null)
  const pending = useRef<{ body: string; key: string } | null>(null)
  const entries: FeedbackEntry[] = [
    ...(feedback.checks ?? []).map((check, index) => ({ key: `check:${index}`, heading: `Check: ${check.name}`, body: `${check.status}${check.conclusion ? ` · ${check.conclusion}` : ''}`, url: check.url })),
    ...(feedback.comments ?? []).map((comment) => ({ key: `comment:${comment.id}`, heading: `Comment by ${comment.author} · ${comment.created_at}`, body: comment.body, url: comment.url })),
    ...(feedback.reviews ?? []).map((review) => ({ key: `review:${review.id}`, heading: `Review by ${review.author}: ${review.state} · ${review.submitted_at}`, body: review.body })),
    ...(feedback.review_comments ?? []).map((comment) => ({ key: `inline:${comment.id}`, heading: `Inline review by ${comment.author}: ${comment.path}:${comment.start_line ? `${comment.start_line}–` : ''}${comment.line ?? comment.original_line ?? '?'} (${comment.side ?? 'unknown side'})`, body: `${comment.body}\nCreated: ${comment.created_at}\nCommit: ${comment.commit_oid}${comment.original_line ? `\nOriginal line: ${comment.original_line}` : ''}${comment.start_side ? `\nStart side: ${comment.start_side}` : ''}${comment.in_reply_to_id ? `\nReply to: ${comment.in_reply_to_id}` : ''}${comment.diff_hunk ? `\n${comment.diff_hunk}` : ''}`, url: comment.url })),
  ]
  const chosen = entries.filter((entry) => selected.includes(entry.key))

  async function send() {
    if (busy || !chosen.length) return
    const pr = feedback.pull_request
    const body = `Selected GitHub feedback${pr ? ` for ${pr.repository}#${pr.number} (${pr.url}), ${pr.head_repository}:${pr.head_branch} → ${pr.base_branch}, head ${pr.head_oid}` : ''}:\n\n${chosen.map((entry) => `${entry.heading}\n${entry.body}${entry.url ? `\n${entry.url}` : ''}`).join('\n\n')}`
    const action = pending.current && pending.current.body === body
      ? pending.current
      : { body, key: crypto.randomUUID() }
    pending.current = action
    setBusy(true)
    setError(null)
    try {
      const result = await client.runRoomPost({ workspace_id: run.workspace_id, run_id: run.id, kind: 'steer_request', body, idempotency_key: action.key })
      useStore.getState().upsertRoomMessage(result.message)
      setReceipt(result.receipt ?? result.message.state)
      setSelected([])
      pending.current = null
    } catch (cause) {
      setError(message(cause))
    } finally { setBusy(false) }
  }

  return <section aria-label="PR feedback" className="space-y-3">
    <p>Feedback identity: {feedback.identity || 'Unavailable'} · Agent account: {feedback.account_member_id}</p>
    <Actual actual={feedback.actual} /><Diagnostics output={feedback.output} error={feedback.error} />
    {feedback.truncated && <p role="alert">Feedback is truncated; open GitHub for the complete discussion.</p>}
    {entries.map((entry) => <article key={entry.key} className="space-y-1 border p-2">
      <label className="flex items-start gap-2"><input type="checkbox" disabled={busy} checked={selected.includes(entry.key)} onChange={() => setSelected((current) => current.includes(entry.key) ? current.filter((key) => key !== entry.key) : [...current, entry.key])} /><span className="min-w-0 break-words font-medium">{entry.heading}</span></label>
      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words font-sans text-xs">{entry.body}</pre>
      {entry.url && <a className="text-xs underline" href={entry.url} target="_blank" rel="noreferrer">Open on GitHub</a>}
    </article>)}
    <p className="text-xs text-muted-foreground">Only checked feedback is sent. The existing Run Room applies steering permission, moderation and delivery state; this does not type directly into the agent.</p>
    <Button size="sm" disabled={busy || !chosen.length} onClick={() => void send()}>Send selected feedback to Run Room</Button>
    {receipt && <p role="status">Run Room delivery: {receipt}. <button className="underline" onClick={() => useStore.getState().navigate('terminal', { runId: run.id })}>Open the run terminal and Run Room</button></p>}
    {error && <p role="alert" className="text-state-failed">{error}</p>}
  </section>
}
