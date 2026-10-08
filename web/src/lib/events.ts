// Names for the event types the server emits, shared by the feed row and the
// Activity filter so a row and its filter option cannot drift.

export const eventLabel = {
  'run.status': 'Run state',
  'run.retention': 'Runtime retention',
  'run.input': 'Agent questions',
  'run.title': 'Run title',
  'run.deleted': 'Run deleted',
  'workspace.deleted': 'Workspace deleted',
  'run.protected': 'Run protection',
  'run.controller': 'Run control',
  'run.mode': 'Run mode',
  'run.archived': 'Archive',
  'run.outcome_seen': 'Result opened',
  'run.agent': 'Agent tool use',
  'run.diff': 'File changes',
  'run.cost': 'Usage',
  'run.overlap': 'Overlapping runs',
  'workspace.timeline': 'Messages and steering',
  'workspace.approval': 'Approvals',
  'workspace.presence': 'Presence',
  'workspace.budget': 'Budget',
  'git.branch': 'Branch updates',
  'sync.conflict': 'Sync conflicts',
  'server.update': 'Server updates',
  'workspace.room_message': 'Run notes',
  'workspace.evidence_packet': 'Saved evidence',
  'coord.message': 'Agent message sent',
  'coord.message.acked': 'Agent message read',
  'mission.changed': 'Swarm changes',
  'member.changed': 'Member renames',
  'profile.change': 'Agent profiles',
} satisfies Record<string, string>

export type EventType = keyof typeof eventLabel

/** A server newer than this dashboard can emit a type the map has never seen. */
export function typeLabel(type: string): string {
  return Object.hasOwn(eventLabel, type) ? eventLabel[type as EventType] : 'Other'
}
