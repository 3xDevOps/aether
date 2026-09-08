// The setup a scenario is not about: an admin who has already linked,
// created the workspace and seeded it. It runs through the same gateway the
// browser drives, so nothing here is a stand-in for the real path - it is
// the real path, without spending a browser on it.

import type { Member } from '../fixtures'
import type { RepoPushResult, WorkspaceResult } from './client'

/** The server's id for whoever this gateway is linked as. */
export async function memberID(member: Member): Promise<string> {
  const info = await member.api.rpc<{ member: { id: string } }>('server.info')
  return info.member.id
}

/**
 * Links the admin, creates a workspace and pushes `repo`'s base branch into
 * it, which is the state a second member joins. Returns the commit the
 * workspace's base branch now points at.
 */
export async function seedWorkspace(
  admin: Member,
  serverAddr: string,
  repo: string,
  name = 'project',
): Promise<string> {
  // On a fresh server the first identity to authenticate becomes the admin.
  await admin.api.local('link.apply', {
    addr: serverAddr,
    name: admin.name,
  })
  const { workspace } = await admin.api.rpc<WorkspaceResult>('workspace.add', {
    name,
    base_branch: 'main',
    environment: {},
  })
  await admin.api.local('link.repo', { repo, workspace_id: workspace.id })
  const push = await admin.api.local<RepoPushResult>('repo.push', {
    workspace_id: workspace.id,
  })
  if (push.state !== 'pushed') {
    throw new Error(`seedWorkspace: repo.push answered ${push.state}: ${push.output}`)
  }
  return push.local_commit
}
