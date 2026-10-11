import type { RunGitPushTarget, RunGitStatusResult, RunPRTarget } from '@/lib/types'

export function githubRepository(url: string | undefined): string {
  return url?.match(/^(?:https:\/\/(?:[^@/]+@)?github\.com\/|(?:ssh:\/\/)?git@github\.com[:/])([^/]+\/[^/]+?)(?:\.git)?\/?$/)?.[1] ?? ''
}

/**
 * Where the checkout itself pushes: the branch's upstream, else `origin` when
 * the workspace set one. Without a workspace Origin a checkout's `origin` is
 * the server's own repository, which is not a place to publish.
 */
export function defaultPushTarget(status: RunGitStatusResult, workspaceOrigin: string | undefined): RunGitPushTarget {
  const remotes = status.remotes ?? []
  const tracked = remotes.find((remote) => remote.name === status.upstream?.remote)
  const remote = tracked ?? (workspaceOrigin ? remotes.find((entry) => entry.name === 'origin') : undefined)
  return {
    remote: remote?.name ?? '',
    repository: remote?.push_urls.length === 1 ? remote.push_urls[0]! : '',
    head_branch: tracked ? status.upstream!.branch : status.branch,
  }
}

/** `source` is where the workspace's code came from, which for a fork Origin is its upstream. */
export function defaultPRTarget(push: RunGitPushTarget, base: string, source: string | undefined): RunPRTarget {
  const head = githubRepository(push.repository)
  if (!head) return { repository: '', base_branch: '', head_repository: '', head_branch: '' }
  return { repository: githubRepository(source) || head, base_branch: base, head_repository: head, head_branch: push.head_branch }
}

export function completeTarget(target: RunPRTarget): boolean {
  return Object.values(target).every((value) => value.trim() !== '')
}
