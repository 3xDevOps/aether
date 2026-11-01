// Names for the event types the server emits, shared by the feed row and the
// Activity filter so a row and its filter option cannot drift.

import type { Event, RunStatusPayload } from '@/lib/types'

export const eventLabel = {
  'run.status': 'Session state',
  'run.retention': 'Runtime retention',
  'run.input': 'Agent questions',
  'run.title': 'Session title',
  'run.deleted': 'Session deleted',
  'workspace.deleted': 'Workspace deleted',
  'run.protected': 'Session protection',
  'run.children': 'Subsessions',
  'run.controller': 'Session control',
  'run.mode': 'Session mode',
  'run.account': 'Account changed',
  'run.usage_limit': 'Usage limit reached',
  'run.model': 'Session model',
  'run.effort': 'Session effort',
  'run.archived': 'Archive',
  'run.outcome_seen': 'Result opened',
  'run.finish_opened': 'Result opened',
  'run.review': 'Completion review',
  'run.agent': 'Agent tool use',
  'run.diff': 'File changes',
  'run.cost': 'Usage',
  'run.overlap': 'Overlapping sessions',
  'workspace.timeline': 'Messages and steering',
  'workspace.presence': 'Presence',
  'workspace.budget': 'Budget',
  'git.branch': 'Branch updates',
  'run.pr': 'Pull requests',
  'sync.conflict': 'Sync conflicts',
  'server.update': 'Server updates',
  'workspace.room_message': 'Session notes',
  'workspace.evidence_packet': 'Saved evidence',
  'coord.message': 'Agent message sent',
  'coord.message.acked': 'Agent message read',
  'member.changed': 'Member renames',
  'member.device': 'Devices',
  'member.merged': 'Member merges',
  'profile.change': 'Agent profiles',
  'memory.change': 'Team memory',
  'watch.changed': 'Watches',
} satisfies Record<string, string>

export type EventType = keyof typeof eventLabel

/** A server newer than this dashboard can emit a type the map has never seen. */
export function typeLabel(type: string): string {
  return Object.hasOwn(eventLabel, type) ? eventLabel[type as EventType] : 'Other'
}

/** A same-status `run.status` whose only news is a newer read of the run's wait: no change of state. */
export function isReread(event: Event): boolean {
  return event.type === 'run.status' && (event.payload as RunStatusPayload | undefined)?.reread === true
}
