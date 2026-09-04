// The client-side mirror of internal/permissions. The server is the
// authority - every verb below is checked again there - but a button the
// server will refuse is worse than no button, so the dashboard asks the same
// questions before it draws one. Keep this file and the Go policy in step:
// the rules are the design doc's role table plus the two restrictions the Go
// package comment names.

import type { Member } from '@/lib/types'

/** One guarded class of action, named as internal/permissions names it. */
export type RunPermission = 'steer' | 'kill' | 'handoff' | 'protect' | 'launch'

/** The member attempting the action. */
export interface Actor {
  id: string | null
  role: Member['role'] | null
}

/** The run being acted on, and the policy its workspace sets. */
export interface Target {
  /** The owning member; absent when the action targets no run. */
  owner?: string
  /** Restricts steer and kill to the run's owner and admins. */
  protected?: boolean
  /** The workspace's steer_others setting; "admins_only" is the strict one. */
  steerOthers?: string
}

export const STEER_OTHERS_ADMINS_ONLY = 'admins_only'

/**
 * Whether this member may take this action. A null role means the caller's
 * own record has not arrived yet: answer yes, because the alternative is a
 * shell whose buttons appear a beat after everything else, and the server
 * still refuses anything this member may not do.
 */
export function allowed(
  permission: RunPermission,
  actor: Actor,
  target: Target = {},
): boolean {
  if (actor.role === null || actor.role === 'admin') return true
  const owner = actor.id !== null && actor.id !== '' && actor.id === target.owner
  switch (permission) {
    case 'launch':
      return actor.role === 'collaborator'
    case 'handoff':
    case 'protect':
      return owner
    case 'steer':
    case 'kill':
      if (actor.role !== 'collaborator') return false
      if (owner) return true
      if (target.protected) return false
      return target.steerOthers !== STEER_OTHERS_ADMINS_ONLY
  }
}
