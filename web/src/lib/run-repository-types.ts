// JSON contracts from internal/protocol/runrepo.go.
import type { Workspace, WorkspaceMirrorAuth, WorkspaceMirrorResult } from '@/lib/types'

export interface SetupPolicy {
  script?: string
}

export interface WorkspaceEnvironment {
  variables?: Record<string, string>
  setup_policy?: SetupPolicy
}

export interface WorkspaceImportParams {
  name: string
  environment: WorkspaceEnvironment
  source_url: string
  base_branch: string
  origin: string
  auth: WorkspaceMirrorAuth
  known_hosts?: string
  github_account_id?: number
}

/** Keep the created workspace even when configuring or fetching its mirror fails. */
export interface WorkspaceImportResult {
  workspace: Workspace
  created: boolean
  mirror: WorkspaceMirrorResult
  error?: string
}

export interface RunGitStatusParams {
  run_id: string
}

export interface RunGitExpected {
  branch: string
  head: string
}

export interface RunGitChange {
  path: string
  original_path?: string
  index: string
  worktree: string
  untracked: boolean
  conflicted: boolean
}

export interface RunGitRemote {
  name: string
  fetch_urls: string[]
  push_urls: string[]
}

export interface RunGitUpstream {
  remote: string
  branch: string
}

export interface RunGitStatusResult {
  branch: string
  head: string
  detached: boolean
  unborn: boolean
  changes: RunGitChange[] | null
  truncated: boolean
  account_member_id: string
  account_name: string
  identity: string
  identity_error?: string
  remotes: RunGitRemote[] | null
  upstream?: RunGitUpstream
  output: RunRepoCommandOutput
  error?: string
}

/** Diagnostic output remains meaningful even after a partially successful mutation. */
export interface RunRepoCommandOutput {
  exit_code: number
  stdout?: string
  stderr?: string
  truncated: boolean
}

export interface RunGitDiffParams {
  run_id: string
  paths?: string[]
  staged?: boolean
}

export interface RunGitDiffResult {
  state: RunGitExpected
  output: RunRepoCommandOutput
  error?: string
}

export interface RunGitCommitParams {
  run_id: string
  expected: RunGitExpected
  paths: string[]
  message: string
}

export interface RunGitCommitResult {
  committed: boolean
  head: string
  index_updated: boolean
  hooks_run: boolean
  output: RunRepoCommandOutput
  error?: string
  actual?: RunGitExpected
}

/** repository is the selected remote's explicit push URL, not a PR repository. */
export interface RunGitPushTarget {
  remote: string
  repository: string
  head_branch: string
}

export interface RunGitPushParams {
  run_id: string
  expected: RunGitExpected
  target: RunGitPushTarget
}

export interface RunGitPushResult {
  pushed: boolean
  head: string
  target: RunGitPushTarget
  output: RunRepoCommandOutput
  error?: string
  actual?: RunGitExpected
}

/** Repository identities are owner/name pairs; head_repository may be a fork. */
export interface RunPRTarget {
  repository: string
  base_branch: string
  head_repository: string
  head_branch: string
}

export interface RunPRStatusParams {
  run_id: string
  expected: RunGitExpected
  target: RunPRTarget
}

export interface RunPRCreateParams {
  run_id: string
  expected: RunGitExpected
  target: RunPRTarget
  title: string
  body: string
  draft?: boolean
  expected_login?: string
}

export interface RunPullRequest {
  number: number
  url: string
  state: string
  title: string
  draft: boolean
  repository: string
  base_branch: string
  head_repository: string
  head_branch: string
  head_oid: string
}

export interface RunPRStatusResult {
  identity: string
  pull_request: RunPullRequest | null
  output: RunRepoCommandOutput
  error?: string
  account_member_id: string
  actual?: RunGitExpected
}

export interface RunPRCreateResult {
  identity: string
  pull_request: RunPullRequest | null
  created: boolean
  reconciled: boolean
  creation_uncertain: boolean
  account_member_id: string
  output: RunRepoCommandOutput
  error?: string
  actual?: RunGitExpected
}

export interface RunPRFeedbackParams {
  run_id: string
  expected: RunGitExpected
  target: RunPRTarget
  limit?: number
}

export interface RunPRCheck {
  name: string
  status: string
  conclusion?: string
  url?: string
}

export interface RunPRComment {
  id: string
  author: string
  body: string
  url: string
  created_at: string
}

export interface RunPRReview {
  id: string
  author: string
  body: string
  state: string
  submitted_at: string
}

export interface RunPRReviewComment {
  id: string
  review_id: string
  author: string
  body: string
  url: string
  created_at: string
  path: string
  line?: number
  original_line?: number
  side?: string
  start_line?: number
  start_side?: string
  commit_oid: string
  in_reply_to_id?: string
  diff_hunk?: string
}

export interface RunPRFeedbackResult {
  identity: string
  pull_request: RunPullRequest | null
  checks: RunPRCheck[]
  comments: RunPRComment[]
  reviews: RunPRReview[]
  review_comments: RunPRReviewComment[]
  account_member_id: string
  actual?: RunGitExpected
  truncated: boolean
  output: RunRepoCommandOutput
  error?: string
}
