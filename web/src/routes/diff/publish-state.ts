import { useEffect, useRef, useState } from 'react'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type {
  FileRead, Run, RunGitCommitResult, RunGitDiffResult, RunGitExpected, RunGitPushResult,
  RunGitPushTarget, RunGitStatusResult, RunPRCreateResult, RunPRStatusResult, RunPRTarget,
} from '@/lib/types'
import { refreshPullRequest, trackPullRequest } from '@/routes/diff/publish-pr'
import { completeTarget, defaultPRTarget, defaultPushTarget } from '@/routes/diff/publish-target'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'

export type PublishAction = 'status' | 'review' | 'commit' | 'push' | 'discover' | 'create'

export interface UntrackedPreview {
  path: string
  file?: FileRead
  error?: string
}

interface PRReview {
  target: RunPRTarget
  expected: RunGitExpected
  result: RunPRStatusResult
}

const noPush: RunGitPushTarget = { remote: '', repository: '', head_branch: '' }

/** Every mutation names the branch and HEAD on screen; the server refuses it
 * when the checkout moved. Destinations start as the checkout's own and stay
 * that way, following each refresh, until the member edits them. */
export function usePublish(run: Run, client: Api, open: boolean) {
  const caps = useCapability()
  const self = useSelf()
  const workspace = useStore((s) => s.workspaces[run.workspace_id])
  const [status, setStatus] = useState<RunGitStatusResult | null>(null)
  const [statusReady, setStatusReady] = useState(false)
  const [untracked, setUntracked] = useState<UntrackedPreview[]>([])
  const [paths, setPaths] = useState<string[]>([])
  const [patches, setPatches] = useState<RunGitDiffResult[] | null>(null)
  const [reviewed, setReviewed] = useState<RunGitExpected | null>(null)
  const [commitMessage, setCommitMessage] = useState('')
  const [commit, setCommit] = useState<RunGitCommitResult | null>(null)
  const [push, setPush] = useState<RunGitPushResult | null>(null)
  const [source, setSource] = useState<string>()
  const [pushEdit, setPushEdit] = useState<RunGitPushTarget | null>(null)
  const [targetEdit, setTargetEdit] = useState<RunPRTarget | null>(null)
  const [prReview, setPRReview] = useState<PRReview | null>(null)
  const [discoverable, setDiscoverable] = useState(false)
  const [creation, setCreation] = useState<RunPRCreateResult | null>(null)
  const [uncertain, setUncertain] = useState(false)
  const [titleEdit, setTitle] = useState<string | null>(null)
  const [body, setBody] = useState('')
  const [draft, setDraft] = useState(false)
  const [action, setAction] = useState<PublishAction | null>(null)
  const busy = action !== null
  const [errors, setErrors] = useState<Partial<Record<PublishAction, string>>>({})
  const mounted = useRef(true)
  const operation = useRef(false)
  const requested = useRef(false)
  const canWrite = self.role === 'admin' || self.role === 'collaborator'
  const expected = statusReady && status && !status.error ? { branch: status.branch, head: status.head } : null
  const pushTarget = pushEdit ?? (status ? defaultPushTarget(status, workspace?.origin) : noPush)
  const target = targetEdit ?? defaultPRTarget(pushTarget, run.base_branch || workspace?.base_branch || '', source)
  const targetComplete = completeTarget(target)
  // A run that has committed nothing still has its base's subject at HEAD.
  const title = titleEdit ?? (status && status.head !== run.base_commit ? status.head_subject ?? '' : '')
  const currentPRReview = prReview && expected && prReview.expected.branch === expected.branch && prReview.expected.head === expected.head &&
    JSON.stringify(prReview.target) === JSON.stringify(target) ? prReview : null

  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  useEffect(() => {
    if (!open || requested.current) return
    requested.current = true
    void perform('status', refreshStatus)
  }, [open])

  const discovers = open && discoverable && !busy && !uncertain && !!expected && targetComplete && !!status?.identity && caps.hasMethod('run.pr.status')
  useEffect(() => {
    if (!discovers) return
    setDiscoverable(false)
    void perform('discover', discoverPR)
  }, [discovers])

  async function perform(action: PublishAction, fn: () => Promise<void>) {
    if (operation.current) return
    operation.current = true
    setAction(action)
    setErrors((all) => ({ ...all, [action]: undefined }))
    try {
      await fn()
    } catch (cause) {
      if (mounted.current) setErrors((all) => ({ ...all, [action]: message(cause) }))
    } finally {
      operation.current = false
      if (mounted.current) setAction(null)
    }
  }

  async function refreshStatus() {
    setStatusReady(false)
    setReviewed(null)
    setPRReview(null)
    const [next, mirror] = await Promise.all([
      client.runGitStatus({ run_id: run.id }),
      // Without the source, the pull request defaults to the repository pushed to.
      client.workspaceMirrorStatus(run.workspace_id).then((result) => result.source_url, () => undefined),
    ])
    if (!mounted.current) return
    setStatus(next)
    setSource(mirror)
    setStatusReady(true)
    setDiscoverable(true)
    setPaths((selected) => selected.filter((path) => (next.changes ?? []).some((change) => change.path === path)))
    setPatches(null)
    setUntracked([])
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
      throw new Error('Branch or HEAD changed during review. Refresh the status and review the selected paths again.')
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

  async function pushBranch() {
    if (!expected) return
    setStatusReady(false)
    const result = await client.runGitPush({ run_id: run.id, expected, target: pushTarget })
    setPush(result)
    if (result.pushed) void refreshPullRequest(run, client)
    await refreshStatus()
  }

  async function discoverPR() {
    if (!expected) return
    setPRReview(null)
    const result = await client.runPRStatus({ run_id: run.id, expected, target })
    if (!mounted.current) return
    setPRReview({ target: { ...target }, expected, result })
    if (!result.pull_request || result.error) return
    setUncertain(false)
    void trackPullRequest(run, target, client)
  }

  async function createPR() {
    if (!currentPRReview || uncertain) return
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
      void trackPullRequest(run, currentPRReview.target, client)
    }
  }

  // An edit waits for Discover: looking a target up on every keystroke would not.
  function changeTarget(key: keyof RunPRTarget, value: string) {
    setTargetEdit({ ...target, [key]: value })
    setDiscoverable(false)
    setPRReview(null)
    setCreation(null)
  }

  function changePushTarget(next: RunGitPushTarget) {
    setPushEdit(next)
    setDiscoverable(false)
  }

  return {
    run, caps, canWrite, busy, action, errors, perform,
    status, statusReady, expected, refreshStatus,
    paths, selectPath, patches, untracked, reviewed, reviewPaths,
    commitMessage, setCommitMessage, commit, commitPaths,
    pushTarget, changePushTarget, push, pushBranch,
    target, targetComplete, changeTarget, currentPRReview, discoverPR, uncertain,
    title, setTitle, body, setBody, draft, setDraft, creation, createPR,
  }
}

export type Publish = ReturnType<typeof usePublish>
