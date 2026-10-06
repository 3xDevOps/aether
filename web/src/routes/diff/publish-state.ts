import { useEffect, useRef, useState } from 'react'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type {
  FileRead, Run, RunGitCommitResult, RunGitDiffResult, RunGitExpected, RunGitPushResult,
  RunGitPushTarget, RunGitStatusResult, RunPRCreateResult, RunPRFeedbackResult,
  RunPRStatusResult, RunPRTarget,
} from '@/lib/types'
import { useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'

export type PublishAction = 'status' | 'review' | 'commit' | 'push' | 'discover' | 'create' | 'feedback' | 'send'

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

/** Every mutation names the branch and HEAD the member reviewed; the server
 * refuses it when the checkout moved, so each refresh drops the attestations. */
export function usePublish(run: Run, client: Api, open: boolean) {
  const caps = useCapability()
  const self = useSelf()
  const [status, setStatus] = useState<RunGitStatusResult | null>(null)
  const [statusReady, setStatusReady] = useState(false)
  const [untracked, setUntracked] = useState<UntrackedPreview[]>([])
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
  const [feedbackSelected, setFeedbackSelected] = useState<string[]>([])
  const [feedbackReceipt, setFeedbackReceipt] = useState<string | null>(null)
  const pendingSend = useRef<{ body: string; key: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<Partial<Record<PublishAction, string>>>({})
  const mounted = useRef(true)
  const operation = useRef(false)
  const requested = useRef(false)
  const canWrite = self.role === 'admin' || self.role === 'collaborator'
  const expected = statusReady && status && !status.error ? { branch: status.branch, head: status.head } : null
  const targetComplete = Object.values(target).every((value) => value.trim() !== '')
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

  async function perform(action: PublishAction, fn: () => Promise<void>) {
    if (operation.current) return
    operation.current = true
    setBusy(true)
    setErrors((all) => ({ ...all, [action]: undefined }))
    try {
      await fn()
    } catch (cause) {
      if (mounted.current) setErrors((all) => ({ ...all, [action]: message(cause) }))
    } finally {
      operation.current = false
      if (mounted.current) setBusy(false)
    }
  }

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

  async function refreshFeedback() {
    setFeedback(null)
    setFeedbackSelected([])
    setFeedbackReceipt(null)
    if (expected) setFeedback(await client.runPRFeedback({ run_id: run.id, expected, target, limit: 100 }))
  }

  function toggleFeedback(key: string) {
    setFeedbackSelected((current) => current.includes(key) ? current.filter((entry) => entry !== key) : [...current, key])
  }

  async function sendFeedback(body: string) {
    // The same message resends under the same key, so a lost response cannot post it twice.
    const action = pendingSend.current?.body === body ? pendingSend.current : { body, key: crypto.randomUUID() }
    pendingSend.current = action
    const result = await client.runRoomPost({ workspace_id: run.workspace_id, run_id: run.id, kind: 'steer_request', body, idempotency_key: action.key })
    pendingSend.current = null
    useStore.getState().upsertRoomMessage(result.message)
    if (!mounted.current) return
    setFeedbackReceipt(result.receipt ?? result.message.state)
    setFeedbackSelected([])
  }

  function changeTarget(key: keyof RunPRTarget, value: string) {
    setTarget((current) => ({ ...current, [key]: value }))
    setPRReview(null)
    setPRReviewed(false)
    setFeedback(null)
    setFeedbackSelected([])
    setFeedbackReceipt(null)
    setCreation(null)
  }

  function changePushTarget(next: RunGitPushTarget) {
    setPushTarget(next)
    setPushReviewed(false)
  }

  return {
    run, caps, canWrite, busy, errors, perform,
    status, statusReady, expected, refreshStatus,
    paths, selectPath, patches, untracked, reviewed, reviewPaths,
    commitMessage, setCommitMessage, commit, commitPaths,
    pushTarget, changePushTarget, pushReviewed, setPushReviewed, push, pushBranch,
    target, targetComplete, changeTarget, currentPRReview, discoverPR, uncertain,
    title, setTitle, body, setBody, draft, setDraft, prReviewed, setPRReviewed, creation, createPR,
    feedback, refreshFeedback, feedbackSelected, toggleFeedback, feedbackReceipt, sendFeedback,
  }
}

export type Publish = ReturnType<typeof usePublish>
