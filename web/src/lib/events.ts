// Human names for the event types the feed can show, shared by the feed row
// and the activity view's type filter so a row and its filter option cannot
// name the same event differently.

/** One entry per type `describe` in `components/feed-entry.tsx` handles. */
export const eventLabel = {
  'run.status': 'Run status',
  'run.title': 'Run title',
  'run.deleted': 'Run deleted',
  'run.protected': 'Run protection',
  'run.agent': 'Agent',
  'run.diff': 'Diff',
  'run.cost': 'Cost',
  'run.overlap': 'Overlap',
  'workspace.timeline': 'Steering',
  'workspace.approval': 'Approval',
  'workspace.presence': 'Presence',
  'workspace.budget': 'Budget',
  'git.branch': 'Branch',
  'sync.conflict': 'Sync conflict',
  'server.update': 'Server update',
} satisfies Record<string, string>

export type EventType = keyof typeof eventLabel

/** A server newer than this dashboard can emit a type the map has never seen. */
export function typeLabel(type: string): string {
  return Object.hasOwn(eventLabel, type) ? eventLabel[type as EventType] : type
}
