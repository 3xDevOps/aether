import { createRoot } from 'react-dom/client'
import '@/index.css'
import { api } from '@/lib/api'
import type { Event, Run } from '@/lib/types'
import { BoardLanes } from '@/routes/board/lanes/board-lanes'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, bob, otherWorkspace, run, serverInfo, workspace } from '@/test/fixtures'

const q = new URLSearchParams(location.search)
const n = Number(q.get('n') ?? 14)
if (q.get('theme') === 'dark') document.documentElement.classList.add('dark')
const now = Date.now()
const min = 60_000
const iso = (ago: number) => new Date(now - ago * min).toISOString()

const titles = ['Implement API', 'Write tests', 'Reviewer', 'Migrate schema', 'Fix flaky CI', 'Docs pass', 'Refactor auth', 'Profile dashboard', 'Add retries', 'Triage issues', 'Bump deps', 'Cache layer']
const runs: Run[] = []
const mk = (id: string, task: string, over: Partial<Run> = {}) =>
  runs.push(run({ id, task, created_at: iso(55), started_at: iso(54), ...over }))
mk('run_lead', 'Lead: plan checkout', { status: 'running', created_at: iso(58), started_at: iso(58) })
mk('run_sub1', 'Reviewer', { parent_run_id: 'run_lead', root_run_id: 'run_lead', started_at: iso(40), created_at: iso(40) })
mk('run_sub2', 'Implement API', { parent_run_id: 'run_lead', root_run_id: 'run_lead', started_at: iso(35), created_at: iso(35) })
mk('run_sub3', 'Write tests', { parent_run_id: 'run_lead', root_run_id: 'run_lead', started_at: iso(30), created_at: iso(30), status: 'completed', state: '', finished_at: iso(12) })
const total = Math.max(n, 6)
for (let i = 0; runs.length < total; i++) {
  const t = titles[i % titles.length] + (i >= titles.length ? ` ${Math.floor(i / titles.length) + 1}` : '')
  const start = 8 + ((i * 7) % 48)
  const kind = i % 4
  mk(`run_${i}`, t, kind === 0
    ? { status: 'completed', state: '', started_at: iso(start + 6), created_at: iso(start + 6), finished_at: iso(start) }
    : kind === 1 ? { status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which port?', started_at: iso(start), created_at: iso(start) }
    : { started_at: iso(start), created_at: iso(start) })
}

let seq = 1000
const mail = (from: string, to: string, ago: number): Event => ({
  id: `evt_${seq}`, seq: seq++, time: new Date(now - ago * 1000).toISOString(), workspace_id: workspace.id, run_id: from, actor_id: '',
  type: 'coord.message', payload: { message_id: `m${seq}`, workspace_id: workspace.id, from_run_id: from, to_run_id: to, kind: 'question' },
})
const hist = [
  mail('run_lead', 'run_sub1', 25 * 60), mail('run_sub1', 'run_lead', 22 * 60), mail('run_lead', 'run_sub2', 18 * 60),
  mail('run_sub2', 'run_sub3', 15 * 60), mail('run_0', 'run_2', 30 * 60), mail('run_4', 'run_lead', 10 * 60),
].filter((e) => runs.some((r) => r.id === e.payload.from_run_id) && runs.some((r) => r.id === e.payload.to_run_id))

;(api as any).workspaceTimeline = async () => ({ events: hist, next_seq: 0, more: false })

useStore.setState({
  workspaces: { [workspace.id]: workspace, [otherWorkspace.id]: otherWorkspace },
  activeWorkspace: workspace.id,
  capabilities: { gateway: 'remote', methods: ['*'], ws: [] },
  info: { ...serverInfo, member: alice },
  members: { [alice.id]: alice, [bob.id]: bob },
  devices: [], runs: Object.fromEntries(runs.map((r) => [r.id, toRecord(r)])),
  agentList: null, pausedRuns: {}, roomMessages: {}, roomStatus: {}, diffs: {}, overlaps: {},
  mineOnly: false, boardViews: {}, boardMapAllWorkspaces: false, boardMapViewports: {},
  hydrated: true, hydrationError: null, paletteDialog: null, lastSeq: 0,
  route: { name: 'board', params: {} },
} as any)

;(window as any).sendMail = (from: string, to: string) => {
  const e = mail(from, to, 0)
  useStore.getState().appendLaneEvent(e)
}
;(window as any).runIds = runs.map((r) => r.id)
createRoot(document.getElementById('root')!).render(<BoardLanes agentNames={{}} />)
