// The assistant's preference notices and Temporary chat against a real
// gateway and server. The assistant agent is not installed here, so
// assistant.status and its stream are scripted: one remember call, shown as a
// Saved card with Undo and Edit, and assistant.settings answers the Temporary
// chat switch. Data is synthetic.

import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

const ready = {
  state: 'idle',
  agent: 'claude',
  agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }],
  confirmations: [],
  auto_approve: false,
  suggest_auto_approve: false,
  share_view: false,
}

const at = (n: number) => new Date(Date.parse('2026-10-27T10:00:00Z') + n * 1000).toISOString()
const items = [
  { seq: 1, epoch: 0, time: at(1), turn: 1, kind: 'turn_start' },
  { seq: 2, epoch: 0, time: at(2), turn: 1, kind: 'message', message: { role: 'user', message_id: 'm1', text: 'Remember that I prefer table output', complete: true } },
  {
    seq: 3, epoch: 0, time: at(3), turn: 1, kind: 'tool_call',
    tool_call: {
      id: 't1', title: 'aether_remember', tool_kind: 'other', status: 'completed', tool_name: 'mcp__aether__aether_remember',
      raw_input: { text: 'Prefer table output' },
      raw_output: { preference: { id: 'pre-scripted', text: 'Prefer table output', enabled: true, source: 'assistant', created_at: at(3), updated_at: at(3) } },
    },
  },
  { seq: 4, epoch: 0, time: at(4), turn: 1, kind: 'turn_end', stop_reason: 'end_turn' },
]

async function script(page: Page) {
  let temporary = false
  await page.route('**/api/v1/assistant.status', (route) => route.fulfill({ json: { ...ready, temporary } }))
  await page.route('**/api/v1/assistant.settings', (route) => {
    temporary = (route.request().postDataJSON() as { temporary?: boolean }).temporary ?? temporary
    return route.fulfill({ json: { auto_approve: false, suggest_auto_approve: false, suggestion_off: false, share_view: false, preferences_enabled: true, temporary } })
  })
  await page.routeWebSocket(/\/ws\/assistant/, (ws) => {
    ws.onMessage(() => {
      const head = { ok: true, seq: 4, replay: items.length, epoch: 0, live: true, has_control: false, state: { turn_in_flight: false, queued: 0, pending: [], last_activity: at(4), prompt_images: false } }
      ws.send([head, ...items.map((item) => ({ seq: item.seq, item }))].map((frame) => JSON.stringify(frame)).join('\n'))
    })
  })
}

async function shot(page: Page, name: string) {
  const dir = process.env.PREFERENCE_SHOTS
  if (dir) await page.screenshot({ path: `${dir}/${name}.png` })
}

test('the assistant shows what it saved with Undo and Edit, and Temporary chat says what it means', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  await script(page)

  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto(alice.url)
  await page.getByRole('button', { name: 'Assistant' }).first().click()
  const assistant = page.getByRole('complementary', { name: 'Assistant' })
  const saved = assistant.getByRole('group', { name: /^Saved: Prefer table output/ })
  await expect(saved).toBeVisible()
  await expect(saved.getByRole('button', { name: 'Undo' })).toBeVisible()
  await expect(saved.getByRole('button', { name: 'Edit' })).toBeVisible()
  await shot(page, 'assistant-saved-desktop')

  await assistant.getByRole('button', { name: 'Temporary chat' }).click()
  await expect(assistant.getByText("Temporary: the assistant won't read or save your preferences until New conversation.")).toBeVisible()
  await shot(page, 'assistant-temporary-desktop')

  await page.setViewportSize({ width: 390, height: 844 })
  const sheet = page.getByRole('dialog', { name: 'Assistant' })
  await expect(sheet.getByRole('group', { name: /^Saved: Prefer table output/ })).toBeVisible()
  await expect(sheet.getByText(/Temporary: the assistant won't read/)).toBeVisible()
  await shot(page, 'assistant-saved-temporary-mobile')
})
