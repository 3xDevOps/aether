import type { ReactNode } from 'react'
import { Participants } from '@/components/messages/message-row'
import type { EventType } from '@/lib/events'
import { budgetStateLabel, money } from '@/lib/format'
import { plainReason } from '@/lib/status'
import type { BudgetState, Event, RoomMessageKind, RoomMessageState, RunStatus } from '@/lib/types'
import { modeLabel } from '@/routes/run/agent-name'
import { useStore } from '@/store'

type Payload = Record<string, unknown>

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function capitalized(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

function bounded(value: unknown, max: number): string {
  const line = text(value)
  return line.length > max ? `${line.slice(0, max)}…` : line
}

export function Who({ id, fallback = 'Someone', object = false }: { id: unknown; fallback?: string; object?: boolean }) {
  const memberID = text(id)
  const self = useStore((s) => s.info?.member.id)
  const name = useStore((s) => s.members[memberID]?.display_name)
  if (memberID && memberID === self) return object ? 'you' : 'You'
  return name ?? fallback
}

function MemberRenamed({ member, actor, name }: { member: unknown; actor: unknown; name: unknown }) {
  const self = useStore((s) => s.info?.member.id)
  const whom = text(member) === text(actor) ? 'themselves' : text(member) === self ? 'you' : 'a teammate'
  return <><Who id={actor} /> renamed {whom}{text(name) && <> to <Quote>{bounded(name, 80)}</Quote></>}</>
}

function MissionName({ id }: { id: unknown }) {
  const objective = useStore((s) => s.missions[text(id)]?.objective)
  return objective ? `“${objective.split('\n')[0]}”` : 'a swarm'
}

const statusWords: Record<RunStatus, string> = {
  queued: 'Run queued',
  provisioning: 'Run starting',
  running: 'Agent working',
  'needs-attention': 'Needs you',
  completed: 'Finished',
  merged: 'Merged',
  abandoned: 'Closed without merging',
  failed: 'Failed',
  interrupted: 'Interrupted',
}

function statusLine(p: Payload): string {
  const to = text(p.to) as RunStatus
  const reason = text(p.reason)
  if (to === 'abandoned' && reason === 'killed') return 'Stopped'
  const word = statusWords[to] ?? 'Run changed state'
  if (!reason || (to === 'running' && reason === 'agent resumed')) return word
  const plain = plainReason(reason)
  return `${word}: ${to === 'needs-attention' ? plain.charAt(0).toLowerCase() + plain.slice(1) : plain}`
}

const steerStates: Record<RoomMessageState, string> = {
  queued: 'is waiting for the controller',
  sent: 'reached the agent',
  not_sent: 'was not sent',
  uncertain: 'may not have reached the agent',
  denied: 'was declined',
  cancelled: 'was cancelled',
}

function roomLine(p: Payload): ReactNode {
  const who = <Who id={p.actor_id} />
  switch (text(p.kind) as RoomMessageKind) {
    case 'comment':
      return <>{who} added a note</>
    case 'question':
      return <>{who} asked a question</>
    case 'reply':
      return <>{who} answered a question</>
    case 'system':
      return 'Aether added a note'
    case 'steer_request': {
      const state = steerStates[text(p.state) as RoomMessageState]
      return state ? <>A message from <Who id={p.actor_id} object /> {state}</> : <>{who} messaged the agent</>
    }
    default:
      return <>{who} posted on the run</>
  }
}

function timelineLine(p: Payload, actor: string): ReactNode {
  const who = <Who id={actor} />
  const message = text(p.message)
  switch (text(p.kind)) {
    case 'steer':
      return <>{who} messaged the agent{message && <>: <Quote>{bounded(message, 240)}</Quote></>}</>
    case 'pause':
      return <>{who} paused the run</>
    case 'resume':
      return <>{who} resumed the run</>
    case 'kill':
      return <>{who} stopped the run</>
    case 'handoff':
      return <>{who} handed the run to <Who id={message} fallback="another member" object /></>
    case 'co-author':
      return <>{who} steered the run and is now a co-author</>
    case 'report':
      return reportLine(p)
    case 'note':
      return capitalized(message) || 'Note'
    default:
      return message ? capitalized(message) : 'Timeline entry'
  }
}

function Quote({ children }: { children: ReactNode }) {
  return <span className="text-muted">“{children}”</span>
}

function reportLine(p: Payload): ReactNode {
  const outcome = bounded(p.outcome, 64)
  const summary = bounded(p.summary, 512)
  const next = text(p.next_action)
  return (
    <span className="inline-flex max-w-full flex-wrap gap-x-2 gap-y-1">
      <span>The agent reported {outcome ? outcome.toLowerCase() : 'its outcome'}{summary && ':'}</span>
      {summary && <span>{summary}</span>}
      {next && <span className="text-muted">Next: {bounded(plainReason(next), 512)}</span>}
    </span>
  )
}

function approvalLine(p: Payload, actor: string): ReactNode {
  const action = text(p.action)
  const what = action ? <>“{bounded(action, 160)}”</> : 'the request'
  switch (text(p.decision)) {
    case 'requested':
      return <>The agent asked to approve {what}</>
    case 'approved':
      return <><Who id={actor} /> approved {what}</>
    case 'denied':
      return <><Who id={actor} /> denied {what}</>
    default:
      return <>Approval for {what} changed</>
  }
}

function agentLine(p: Payload): string {
  const detail = bounded(p.detail, 160)
  switch (text(p.kind)) {
    case 'tool_call':
      return text(p.verb) ? [text(p.verb), detail].filter(Boolean).join(' ') : `Used ${text(p.tool) || 'a tool'}${detail ? `: ${detail}` : ''}`
    case 'tool_result':
      return p.is_error ? 'A tool call failed' : 'A tool call finished'
    case 'subagent':
      return `Started a subagent${detail ? `: ${detail}` : ''}`
    case 'pause':
      return `Paused for your review${detail ? `: ${detail}` : ''}`
    case 'session':
      return 'The agent started its session'
    default:
      return detail || 'Agent activity'
  }
}

const presenceWords: Record<string, string> = {
  online: 'came online',
  offline: 'went offline',
  watching: 'opened the run',
}

const updateWords: Record<string, (version: string) => string> = {
  scheduled: (v) => `Server update${v} scheduled for when no run is active`,
  applying: (v) => `Downloading the server update${v}`,
  restarting: (v) => `Server restarting${v}`,
  failed: (v) => `Server update${v} failed`,
  cancelled: (v) => `Server update${v} cancelled`,
}

function updateLine(p: Payload): string {
  const version = text(p.version) ? ` to ${text(p.version)}` : ''
  const line = (updateWords[text(p.phase)] ?? ((v: string) => `Server update${v}`))(version)
  return text(p.detail) ? `${line}: ${text(p.detail)}` : line
}

const captureWords: Record<string, string> = {
  finish: "Saved the run's results",
  handoff: 'Saved the run for a handoff',
  report: "Saved evidence for the agent's report",
}

const profileWords: Record<string, string> = {
  put: 'saved',
  rollback: 'rolled back',
  pin: 'pinned',
}

// An absent or empty `with` means the run's overlaps cleared.
function overlapLine(peers: unknown): string {
  if (!Array.isArray(peers) || peers.length === 0) return 'No longer edits the same files as another run'
  return `Edits the same files as ${peers.length} other ${peers.length === 1 ? 'run' : 'runs'}`
}

function budgetLine(p: Payload): string {
  const spend = money.format(Number(p.spend_usd ?? 0))
  const cap = Number(p.limit_usd ?? 0)
  const spent = `${Number(p.unmetered_runs ?? 0) > 0 ? 'at least ' : ''}${spend} spent${cap > 0 ? ` of ${money.format(cap)}` : ''}`
  const state = budgetStateLabel[p.state as BudgetState]
  const line = `Budget ${state ? `${state}, ` : ''}${spent}`
  return text(p.reason) ? `${line}: ${text(p.reason)}` : line
}

function files(value: unknown): string {
  const count = Array.isArray(value) ? value.length : 0
  return `${count} ${count === 1 ? 'file' : 'files'}`
}

const tokens = new Intl.NumberFormat()

// Keyed by `EventType` so a type without words is a compile error.
const describers: Record<EventType, (p: Payload, event: Event) => ReactNode> = {
  'run.status': (p) => statusLine(p),
  'run.input': (p) => Array.isArray(p.pending_inputs) && p.pending_inputs.length > 0
    ? `The agent is waiting on ${p.pending_inputs.length === 1 ? 'an answer' : `${p.pending_inputs.length} answers`}`
    : "The agent's questions were answered",
  'run.deleted': () => 'Run deleted',
  'workspace.deleted': () => 'Workspace deleted',
  'run.protected': (p, e) => <><Who id={e.actor_id} /> {p.protected ? 'protected the run' : 'removed the run’s protection'}</>,
  'run.controller': (p, e) => {
    if (p.member_id) return <><Who id={p.member_id} /> took control</>
    return e.actor_id ? <><Who id={e.actor_id} /> released control</> : 'Control released'
  },
  'run.mode': (p) => modeLine(p),
  'run.archived': (p, e) => <><Who id={e.actor_id} fallback="Aether" /> {p.archived_at ? 'archived the run' : 'restored the run'}</>,
  'run.outcome_seen': (_, e) => <><Who id={e.actor_id} fallback="The owner" /> opened the finished run</>,
  'run.title': (p) => <>Agent set the title to <Quote>{bounded(p.title, 160)}</Quote></>,
  'run.agent': (p) => agentLine(p),
  'run.diff': (p) => `${files(p.files)} changed`,
  'run.cost': (p) => {
    const used = `Used ${tokens.format(Number(p.input_tokens ?? 0))} input and ${tokens.format(Number(p.output_tokens ?? 0))} output tokens`
    return p.metered && Number(p.cost_usd) > 0 ? `${used}, ${money.format(Number(p.cost_usd))}` : used
  },
  'run.overlap': (p) => overlapLine(p.with),
  'workspace.timeline': (p, e) => timelineLine(p, e.actor_id),
  'workspace.approval': (p, e) => approvalLine(p, e.actor_id),
  'workspace.presence': (p, e) => <><Who id={e.actor_id} /> {presenceWords[text(p.state)] ?? 'changed presence'}</>,
  'workspace.budget': (p) => budgetLine(p),
  'git.branch': (p) => `Branch ${text(p.branch) || 'of the run'} moved${text(p.commit) ? ` to ${text(p.commit).slice(0, 7)}` : ''}`,
  'sync.conflict': (p) => `Live sync paused: ${files(p.files)} changed on both sides`,
  'server.update': (p) => updateLine(p),
  'workspace.room_message': (p) => roomLine(p),
  'workspace.evidence_packet': (p) => captureWords[text(p.trigger)] ?? 'Saved evidence for the run',
  'coord.message': (p) => (
    <span className="inline-flex max-w-full flex-wrap items-center gap-x-1.5">
      <Participants from={text(p.from_run_id)} to={text(p.to_run_id)} />
      <span className="text-muted">· {messageKinds[text(p.kind)] ?? 'Message'}</span>
    </span>
  ),
  'coord.message.acked': () => 'An agent read its message',
  'mission.changed': (p) => (p.deleted ? 'A swarm was deleted' : <>Swarm <MissionName id={p.mission_id} /> updated</>),
  'member.changed': (p, e) => <MemberRenamed member={p.member_id} actor={e.actor_id} name={p.display_name} />,
  'profile.change': (p, e) => <><Who id={p.member ?? e.actor_id} /> {profileWords[text(p.action)] ?? 'changed'} the {text(p.harness) || 'agent'} profile</>,
}

const messageKinds: Record<string, string> = {
  message: 'Message',
  question: 'Question',
  reply: 'Reply',
  report: 'Report',
}

function modeLine(p: Payload): string {
  const mode = modeLabel[text(p.mode)] ?? 'another mode'
  if (p.switching) return `Switching to ${mode}`
  return p.reason ? `Stayed ${mode}: ${text(p.reason)}` : `Now ${mode}`
}

export function describeEvent(event: Event): ReactNode {
  const describe = Object.hasOwn(describers, event.type) ? describers[event.type as EventType] : undefined
  return describe ? describe((event.payload ?? {}) as Payload, event) : 'Something changed'
}
