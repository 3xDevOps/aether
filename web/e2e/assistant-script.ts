// A scripted assistant for the dashboard suite. The assistant agent runs in
// the member's environment container with a real provider sign-in, which CI
// does not have, so these journeys answer assistant.status, assistant.prompt
// and assistant.confirm, and play the agent's side of /ws/assistant: the
// same ack, status and item frames the server sends. Everything else - the
// failed run, the run the assistant launches, the deep link it opens - is
// the real server's.

import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import path from 'node:path'
import type { Page, TestInfo, WebSocketRoute } from '@playwright/test'
import { expect } from './fixtures'
import type { Aether, Member } from './fixtures'
import { seedWorkspace } from './harness/setup'

const task = 'repair the failing importer'
const fix = 'fix the importer and push a branch'

function status(confirmations: unknown[] = [], state = 'idle') {
  return {
    state,
    agent: 'claude',
    agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }],
    confirmations,
    auto_approve: false,
    suggest_auto_approve: false,
    share_view: true,
  }
}

let seq = 0
function frame(turn: number, kind: string, fields: Record<string, unknown>) {
  seq++
  return { seq, item: { seq, epoch: 0, time: new Date().toISOString(), turn, kind, ...fields } }
}

const launchCall = (status: string, extra: Record<string, unknown>) =>
  frame(1, 'tool_call', { tool_call: { id: 'call-launch', title: 'aether_launch_run', tool_kind: 'other', status, tool_name: 'mcp__aether__aether_launch_run', ...extra } })

async function failedRun(aether: Aether, alice: Member) {
  const repo = await aether.seedRepo('project')
  writeFileSync(path.join(repo, 'agent.sh'), 'echo importer-broke\nexit 3\n')
  execFileSync('git', ['-C', repo, '-c', 'user.name=E2E', '-c', 'user.email=e2e@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qam', 'fail'])
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string; name: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0]!.id, harness: 'fake', mode: 'headless', task })
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { status: string } }>('run.get', { run_id: run.id })).run.status, { timeout: 180_000, intervals: [250, 500, 1_000, 2_000] })
    .toBe('failed')
  return { workspace: workspaces[0]!, run }
}

/**
 * Fix this on a failed run, through the assistant's approval card to the
 * action card's deep link into the run it launched.
 */
export async function fixThisJourney(page: Page, aether: Aether, testInfo: TestInfo, shot: string) {
  const alice = await aether.member('alice')
  const { workspace, run } = await failedRun(aether, alice)

  let socket: WebSocketRoute | undefined
  const prompts: { text: string; view_context?: { attachments?: unknown[] } }[] = []
  const confirmed: { confirmation_id: string; approve: boolean }[] = []
  await page.route('**/api/v1/assistant.status', (route) => route.fulfill({ json: status() }))
  await page.route('**/api/v1/assistant.prompt', (route) => {
    prompts.push(route.request().postDataJSON() as (typeof prompts)[number])
    return route.fulfill({ json: {} })
  })
  await page.route('**/api/v1/assistant.confirm', (route) => {
    confirmed.push(route.request().postDataJSON() as (typeof confirmed)[number])
    return route.fulfill({ json: {} })
  })
  await page.routeWebSocket(/\/ws\/assistant/, (ws) => {
    socket = ws
    ws.onMessage(() => ws.send(JSON.stringify({ ok: true, seq: 0, replay: 0, epoch: 0, live: true, has_control: false, state: { turn_in_flight: false, queued: 0, pending: [], prompt_images: false } })))
  })
  const send = (...frames: unknown[]) => socket!.send(frames.map((f) => JSON.stringify(f)).join('\n'))

  await page.goto(`${alice.url}&run=${run.id}`)
  await expect(page.getByRole('heading', { level: 1, name: task, exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'More', exact: true }).first().click()
  await page.getByRole('menuitem', { name: 'Ask the assistant to fix this run' }).click()

  const assistant = page.locator('#assistant')
  const box = assistant.getByRole('textbox', { name: 'Message the assistant' })
  await expect(box).toHaveValue('Fix this')
  await expect(assistant.getByRole('list', { name: 'Sent with this message' }).getByRole('button', { name: `Remove run ${task} from the message` })).toBeVisible()
  await expect.poll(() => socket !== undefined).toBe(true)
  await assistant.getByRole('button', { name: /^Send/ }).click()
  await expect.poll(() => prompts.length).toBe(1)
  expect(prompts[0]!.view_context?.attachments).toEqual([{ kind: 'run', run_id: run.id, workspace_id: workspace.id }])

  const params = { workspace_id: workspace.id, harness: 'fake', mode: 'headless', task: fix }
  send(
    frame(1, 'turn_start', {}),
    frame(1, 'message', { message: { role: 'user', message_id: 'u1', text: 'Fix this', complete: true } }),
    frame(1, 'message', { message: { role: 'assistant', message_id: 'a1', text: `Run ${run.id} failed: the agent exited 3 after printing importer-broke. One run fits: one coherent change.`, complete: true } }),
    launchCall('in_progress', { raw_input: params }),
    { status: status([{ id: 'conf-1', method: 'run.launch', summary: `Launch fake (headless) in ${workspace.name}: "${fix}"`, params, created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 600_000).toISOString() }], 'running') },
  )
  const card = assistant.getByRole('group', { name: /^Approve this action\?/ })
  const starts = card.getByRole('region', { name: 'What starts' })
  await expect(starts).toContainText('Run: one agent on one task')
  await expect(starts).toContainText(workspace.name)
  await expect(starts).toContainText('Yours')
  await expect(starts).toContainText('Any account you may launch on, most usage left first')
  await expect(starts).toContainText(fix)
  await expect(assistant.getByRole('group', { name: /^Launching run on .*Waiting for your approval$/ })).toBeVisible()
  await card.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath(`${shot}-approval.png`) })
  await card.getByRole('button', { name: 'Approve' }).click()
  await expect.poll(() => confirmed).toEqual([{ confirmation_id: 'conf-1', approve: true }])

  const { run: launched } = await alice.api.rpc<{ run: { id: string } }>('run.launch', params)
  send(
    { status: status([], 'running') },
    frame(1, 'notice', { notice: { severity: 'info', title: `Approved: Launch fake (headless) in ${workspace.name}`, description: 'run.launch passed after you approved its card.' } }),
    launchCall('completed', { raw_input: params, content: [{ type: 'text', text: `Launched run ${launched.id}.\n${JSON.stringify({ run_id: launched.id, state: 'queued', workspace_id: workspace.id, harness: 'fake', url: `?run=${launched.id}` })}` }] }),
    frame(1, 'message', { message: { role: 'assistant', message_id: 'a2', text: `Launched run ${launched.id} in ${workspace.name}.`, complete: true } }),
    frame(1, 'turn_end', { stop_reason: 'end_turn' }),
    { status: status() },
  )
  const action = assistant.getByRole('group', { name: /^Launched run on / })
  await expect(action).toContainText(`run · ${workspace.name} · fake`)
  await expect(action).toContainText('approved by you')
  // The launched run fails at once on the seeded agent.sh; its card loses the working-in-the-background line when it does, which would move Open run under the click.
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { status: string } }>('run.get', { run_id: launched.id })).run.status, { timeout: 180_000, intervals: [250, 500, 1_000, 2_000] })
    .toBe('failed')
  await expect(action).toContainText('Done')
  await action.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath(`${shot}-action.png`) })
  await action.getByRole('button', { name: 'Open run' }).click()
  await expect(page).toHaveURL(new RegExp(`[?&]run=${launched.id}\\b`))
  await expect(page.getByRole('heading', { level: 1, name: fix, exact: true })).toBeVisible()
  return { launched: launched.id }
}
