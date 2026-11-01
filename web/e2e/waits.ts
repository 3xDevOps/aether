// Real runs whose agents report a CI wait, shown with the evidence the
// scheduler would attach to each (`run.wait`, internal/scheduler/wait.go).
// The condition engine that reads pull requests is not in this tree yet, so
// the evidence is set on the server's own run.list and run.get answers.

import type { Page } from '@playwright/test'

import type { Aether, Member } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

// The run may still be provisioning, so the report retries; the Stop hook
// then ends the turn on the reported wait.
const waitingAgent = `end=$(($(date +%s) + 180))
until aether-internal report --outcome waiting --for ci --summary 'CI on #582' --idempotency-key wait-fixture >/dev/null; do
  if [ "$(date +%s)" -ge "$end" ]; then exit 1; fi
  sleep 0.1
done
printf '%s\\n' '{"hook_event_name":"Stop"}' | /opt/aether/aether-server report claude
sleep 600`

export const spec = 'pr:acme/web#582:checks'
export const unreadable = 'gh: HTTP 502: Bad Gateway (https://api.github.com/graphql)'
export const missing = 'pull request acme/web#582 not found'
export const undelivered = 'deliver wake: the agent session closed'

export const tasks = {
  condition: 'Port checkout to the payments API',
  unknown: 'Rename the billing tables',
  fault: 'Drop the legacy receipt template',
  broken: 'Bump the payments SDK',
  stale: 'Split the cart service',
} as const

export type Kind = keyof typeof tasks

const minutesAgo = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()

function evidence(kind: Kind): Record<string, unknown> {
  const ci = { status: 'needs-attention', state: 'waiting-ci', reason: 'CI on #582', cause: 'report' }
  switch (kind) {
    case 'condition':
      return { ...ci, wait: { kind: 'condition', detail: `${spec}: open, 3 checks running`, verified_at: minutesAgo(2) } }
    case 'unknown':
      return { ...ci, wait: { kind: 'source_unknown', detail: `${spec}: status unknown`, verified_at: minutesAgo(14), error: unreadable } }
    case 'fault':
      return { ...ci, wait: { kind: 'system_fault', detail: `${spec}: error, waking the agent`, error: missing } }
    case 'broken':
      return {
        status: 'needs-attention', state: 'needs-you', cause: 'wait-fault',
        reason: `wait broken: ${spec} resolved, but the agent was not woken`,
        wait: { kind: 'system_fault', detail: `${spec} resolved, but the agent was not woken (all 3 checks passed)`, error: undelivered },
      }
    case 'stale':
      return { status: 'needs-attention', state: 'waiting', reason: 'no update for 32m: CI on #590', cause: 'wait-expired' }
  }
}

/** Launches one run per kind and waits until each agent's CI wait is the server's state. */
export async function launchWaits(aether: Aether): Promise<{ alice: Member; ids: Record<Kind, string> }> {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', waitingAgent)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const ids = {} as Record<Kind, string>
  for (const kind of Object.keys(tasks) as Kind[]) {
    const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0].id, harness: 'claude', task: tasks[kind] })
    ids[kind] = run.id
  }
  const deadline = Date.now() + 180_000
  for (const id of Object.values(ids)) {
    for (;;) {
      const { run } = await alice.api.rpc<{ run: { state?: string } }>('run.get', { run_id: id })
      if (run.state === 'waiting-ci') break
      if (Date.now() > deadline) throw new Error(`run ${id} never showed its CI wait: state ${run.state}`)
      await new Promise((resolve) => setTimeout(resolve, 500))
    }
  }
  return { alice, ids }
}

/** Sets each run's wait evidence on the server's answers to the dashboard. */
export async function showEvidence(page: Page, ids: Record<Kind, string>) {
  const byID = new Map((Object.keys(ids) as Kind[]).map((kind) => [ids[kind], evidence(kind)]))
  const patch = (run: { id: string }) => ({ ...run, ...byID.get(run.id) })
  await page.route('**/api/v1/run.list', async (route) => {
    const response = await route.fetch()
    const body = (await response.json()) as { runs: { id: string }[] }
    await route.fulfill({ response, json: { ...body, runs: body.runs.map(patch) } })
  })
  await page.route('**/api/v1/run.get', async (route) => {
    const response = await route.fetch()
    const body = (await response.json()) as { run?: { id: string } }
    await route.fulfill({ response, json: body.run ? { ...body, run: patch(body.run) } : body })
  })
}
