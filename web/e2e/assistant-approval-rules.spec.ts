// The dashboard's wiring of approval rules and the setup question, against a
// real gateway and server. The assistant agent is not installed here, so
// assistant.status, its stream, assistant.confirm and the rules list are
// scripted the way the server answers them: this covers the buttons, the
// params they send and the Settings list, not the server's matching, which
// TestIntegrationAssistantApprovalRule (internal/server) drives through the
// real tool socket. The setup question's answer and Approve everything's
// confirm go to the real server's assistant.settings.

import type { Page, WebSocketRoute } from '@playwright/test'
import { expect, test } from './fixtures'

const at = () => new Date().toISOString()
const later = () => new Date(Date.now() + 600_000).toISOString()

const wording = 'Always approve launches of Claude Code (acp) in api on your account, permission default, no fallback'

function launchCard(id: string, task: string, rule?: { wording: string; suggested?: boolean }) {
  return {
    id,
    method: 'run.launch',
    summary: `Launch claude (acp) in api, no fallback: "${task}"`,
    params: { workspace_id: 'ws-scripted', harness: 'claude', mode: 'acp', no_fallback: true, task },
    created_at: at(),
    expires_at: later(),
    ...(rule && { rule }),
  }
}

interface Rule { id: string; method: string; wording: string; summary: string; created_at: string; last_used_at?: string; uses: number }

async function script(page: Page, chosen: () => Promise<boolean>) {
  let socket: WebSocketRoute | undefined
  const confirmed: { confirmation_id: string; approve: boolean; always?: boolean }[] = []
  const revoked: string[] = []
  let rules: Rule[] = []
  const base = { state: 'idle', agent: 'claude', agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }], auto_approve: false, suggest_auto_approve: false, share_view: true }
  await page.route('**/api/v1/assistant.status', async (route) => route.fulfill({ json: { ...base, confirmations: [], approval_chosen: await chosen() } }))
  await page.route('**/api/v1/assistant.confirm', (route) => {
    const body = route.request().postDataJSON() as (typeof confirmed)[number]
    confirmed.push(body)
    if (!body.always) return route.fulfill({ json: {} })
    const rule = { id: 'rule-scripted', method: 'run.launch', wording, summary: launchCard('', 'first').summary, created_at: at(), uses: 0 }
    rules = [rule]
    return route.fulfill({ json: { rule } })
  })
  await page.route('**/api/v1/assistant.rules', (route) => route.fulfill({ json: { rules } }))
  await page.route('**/api/v1/assistant.rules.revoke', (route) => {
    const { id } = route.request().postDataJSON() as { id: string }
    revoked.push(id)
    rules = rules.filter((r) => r.id !== id)
    return route.fulfill({ json: {} })
  })
  await page.routeWebSocket(/\/ws\/assistant/, (ws) => {
    socket = ws
    ws.onMessage(() => ws.send(JSON.stringify({ ok: true, seq: 0, replay: 0, epoch: 0, live: true, has_control: false, state: { turn_in_flight: false, queued: 0, pending: [], prompt_images: false } })))
  })
  const cards = async (...confirmations: unknown[]) => {
    await expect.poll(() => socket !== undefined).toBe(true)
    socket!.send(JSON.stringify({ status: { ...base, confirmations, approval_chosen: await chosen() } }))
  }
  const used = () => {
    rules = rules.map((r) => ({ ...r, uses: r.uses + 1, last_used_at: at() }))
  }
  return { confirmed, revoked, cards, used }
}

test('Always approve saves a rule, a matching launch passes, another kind asks, and revoking brings the card back', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const settings = () => alice.api.rpc<{ auto_approve: boolean; approval_chosen?: boolean }>('assistant.settings')
  expect((await settings()).approval_chosen).toBe(false)
  const { confirmed, revoked, cards, used } = await script(page, async () => (await settings()).approval_chosen ?? false)

  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto(alice.url)
  await page.getByRole('button', { name: 'Assistant' }).first().click()
  const assistant = page.getByRole('complementary', { name: 'Assistant' })

  const question = assistant.getByRole('group', { name: 'How should your assistant act?' })
  await expect(question).toContainText('launch sessions, message and stop your sessions and save your environment on its own')
  await question.getByRole('button', { name: 'Ask before acting' }).click()
  await expect(question).toBeHidden()
  await expect.poll(async () => settings()).toMatchObject({ auto_approve: false, approval_chosen: true })
  await page.reload()
  await page.getByRole('button', { name: 'Assistant' }).first().click()
  await expect(assistant.getByRole('region', { name: 'Suggestions' })).toBeVisible()
  await expect(question).toBeHidden()

  await cards(launchCard('conf-1', 'first', { wording }))
  const first = assistant.getByRole('group', { name: /^Approve this action\? Launch claude .*"first"/ })
  await expect(first.getByRole('button', { name: 'Approve', exact: true })).toBeVisible()
  await expect(first.getByRole('button', { name: 'Decline' })).toBeVisible()
  await expect(first.getByRole('button', { name: 'Approve everything from now on' })).toBeVisible()
  await first.getByRole('button', { name: wording }).click()
  await expect.poll(() => confirmed).toEqual([{ confirmation_id: 'conf-1', approve: true, always: true }])

  // The server passes the next launch of the kind without a card.
  used()
  await cards()
  await expect(first).toBeHidden()

  await cards(launchCard('conf-2', 'another kind', { wording: wording.replace('(acp)', '(tui)') }))
  const other = assistant.getByRole('group', { name: /^Approve this action\? .*"another kind"/ })
  await other.getByRole('button', { name: 'Decline' }).click()
  await expect.poll(() => confirmed.at(-1)).toEqual({ confirmation_id: 'conf-2', approve: false })
  await cards()

  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'Settings', exact: true }).click()
  await page.getByRole('navigation', { name: 'Settings' }).getByRole('button', { name: 'Assistant', exact: true }).click()
  const section = page.getByRole('region', { name: 'Approval rules' })
  await expect(section).toContainText(wording)
  await expect(section).toContainText('From: Launch claude (acp) in api, no fallback: "first"')
  await expect(section).toContainText('once')
  await section.getByRole('button', { name: `Revoke ${wording}` }).click()
  await expect.poll(() => revoked).toEqual(['rule-scripted'])
  await expect(section).toContainText('No rules yet')

  if (!(await assistant.isVisible())) await page.getByRole('button', { name: 'Assistant' }).first().click()
  await cards(launchCard('conf-3', 'after the revoke', { wording }))
  const restored = assistant.getByRole('group', { name: /^Approve this action\? .*"after the revoke"/ })
  await expect(restored.getByRole('button', { name: wording })).toBeVisible()

  await restored.getByRole('button', { name: 'Approve everything from now on' }).click()
  await expect.poll(() => confirmed.at(-1)).toEqual({ confirmation_id: 'conf-3', approve: true })
  const dialog = page.getByRole('alertdialog', { name: 'Let the assistant act without asking?' })
  await dialog.getByRole('button', { name: 'Turn on' }).click()
  await expect(dialog).toBeHidden()
  await expect.poll(async () => (await settings()).auto_approve).toBe(true)
})
