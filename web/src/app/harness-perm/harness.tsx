'use client'
import { AppShell } from '@/components/shell/app-shell'
import { useStore } from '@/store'

const params = new URLSearchParams(window.location.search)
document.documentElement.classList.add('dark')
document.documentElement.dataset.theme = 'dark'

const alice = { id: 'mem_alice', display_name: 'Alice', color: '#e6194b', role: 'super_admin' }
const bob = { id: 'mem_bob', display_name: 'Bob', color: '#3cb44b', role: 'collaborator' }
const vera = { id: 'mem_vera', display_name: 'Vera', color: '#4363d8', role: 'viewer' }
const ida = { id: 'mem_ida', display_name: 'Ida', color: '#f58231', role: 'admin' }
const sam = { id: 'mem_sam', display_name: 'Sam Okafor', color: '#911eb4', role: 'collaborator' }
const root = { id: 'mem_root', display_name: 'Rae', color: '#46f0f0', role: 'super_admin' }
const members = [alice, bob, vera, ida, sam, root]
const ws = [
  { id: 'wsp_1', name: 'main-repo', base_branch: 'main', created_at: '2026-08-14T08:00:00Z', owner_member_id: alice.id, role: 'admin' },
  { id: 'wsp_2', name: 'docs-site', base_branch: 'trunk', created_at: '2026-08-14T08:00:00Z', owner_member_id: ida.id, role: 'admin' },
]
let grants = [{ member_id: bob.id, created_at: '2026-11-10T10:00:00Z' }]
let sharedWith = [vera]
const answers: Record<string, (p: Record<string, unknown>) => unknown> = {
  'workspace.list': () => ({ workspaces: ws }),
  'member.list': () => ({ members }),
  'account.list': () => ({ accounts: [bob], shared_with: sharedWith }),
  'account.share': (p) => { sharedWith = [...sharedWith, members.find((m) => m.id === p.member_id)!]; return {} },
  'account.revoke': (p) => { sharedWith = sharedWith.filter((m) => m.id !== p.member_id); return {} },
  'workspace.access.list': () => ({ workspace_id: 'wsp_1', owner_member_id: alice.id, grants }),
  'workspace.access.grant': (p) => { grants = [...grants, ...(p.member_ids as string[]).map((member_id) => ({ member_id, created_at: '2026-11-12T10:00:00Z' }))]; return {} },
  'workspace.access.revoke': (p) => { grants = grants.filter((g) => g.member_id !== p.member_id); return {} },
  'run.list': () => ({ runs: [] }),
  'attention.list': () => ({ items: [] }),
  'presence.list': () => ({ presence: [] }),
  'workspace.environment.get': () => ({ workspace_id: 'wsp_1', setup_script: 'bun install', variables: [] }),
  'workspace.child_limits': () => ({ max_live: 8 }),
  'agent.list': () => ({ agents: [{ name: 'claude', display_name: 'Claude Code', source: 'shipped', installed: true, enhanced: 'adapter', enhanced_installed: true, login_found: true, default_mode: 'acp' }] }),
  'launch.defaults.get': () => ({ revision: 0, defaults: {} }),
  'assistant.status': () => ({ state: 'idle', agents: [], confirmations: [] }),
  'presence.roster': () => ({ members: [] }),
  'presence.heartbeat': () => ({ ttl_seconds: 60 }),
  'workspace.mirror.status': () => ({ enabled: false }),
  'member.invitation.list': () => ({ invitations: [] }),
  'budget.get': () => ({ workspace_id: 'wsp_1', state: 'ok', spend: { runs: 0, metered_runs: 0, unmetered_runs: 0, input_tokens: 0, output_tokens: 0, cost_usd: 0 } }),
}
const realFetch = window.fetch.bind(window)
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = String(input)
  const method = url.split('/api/v1/')[1]
  if (!method) return realFetch(input, init)
  const answer = answers[method]
  if (!answer) console.warn('unstubbed', method)
  const body = answer ? answer(init?.body ? JSON.parse(String(init.body)) : {}) : {}
  return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } })
}
class StubSocket {
  static OPEN = 1
  readyState = 1
  onopen: (() => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly url: string) { setTimeout(() => this.onopen?.(), 20) }
  send() {}
  close() {}
  addEventListener() {}
  removeEventListener() {}
}
;(window as unknown as { WebSocket: unknown }).WebSocket = StubSocket

const route = params.get('route') ?? 'workspaces'
useStore.setState({
  info: { server_version: '0.8.7', protocol_version: '1', time: '2026-11-12T10:05:00Z', member: alice, features: ['workspace-access'] } as never,
  members: Object.fromEntries(members.map((m) => [m.id, m])) as never,
  workspaces: Object.fromEntries(ws.map((w) => [w.id, w])) as never,
  activeWorkspace: 'wsp_1',
  capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach', 'terminal'] },
  connection: 'live' as never,
  hydrated: true,
  route: { name: route, params: route === 'workspace' ? { workspaceId: 'wsp_1' } : {} },
})

export default function Harness() {
  return <AppShell />
}
