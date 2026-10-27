// "Fix this" on a failed run against a real gateway and server: the run menu
// opens the assistant with the run attached and a draft to edit, the chips and
// Inspect show it, and Send carries exactly that payload. The assistant agent
// is not installed here, so assistant.status is answered as ready, its stream
// is held, and the prompt is captured instead of reaching an agent.

import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import path from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

const task = 'repair the failing importer'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

const ready = {
  state: 'idle',
  agent: 'claude',
  agents: [{ name: 'claude', display_name: 'Claude Code', ready: true }],
  confirmations: [],
  auto_approve: false,
  suggest_auto_approve: false,
  share_view: true,
}

async function askToFix(page: Page) {
  await page.getByRole('button', { name: 'More', exact: true }).first().click()
  await page.getByRole('menuitem', { name: 'Ask the assistant to fix this run' }).click()
}

test('Fix this on a failed run attaches the run and sends exactly what Inspect shows', async ({ page, aether }, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  writeFileSync(path.join(repo, 'agent.sh'), 'echo importer-broke\nexit 3\n')
  execFileSync('git', ['-C', repo, '-c', 'user.name=E2E', '-c', 'user.email=e2e@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qam', 'fail'])
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0].id, harness: 'fake', mode: 'headless', task })
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { status: string } }>('run.get', { run_id: run.id })).run.status, { timeout: 180_000, intervals: [250, 500, 1_000, 2_000] })
    .toBe('failed')

  const prompts: { text: string; view_context?: unknown }[] = []
  await page.route('**/api/v1/assistant.status', (route) => route.fulfill({ json: ready }))
  // The real stream would report the uninstalled agent's setup state over the stub.
  await page.routeWebSocket(/\/ws\/assistant/, () => {})
  await page.route('**/api/v1/assistant.prompt', (route) => {
    prompts.push(route.request().postDataJSON() as { text: string; view_context?: unknown })
    return route.fulfill({ json: {} })
  })

  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto(`${alice.url}&run=${run.id}`)
  await expect(page.getByRole('heading', { level: 1, name: task, exact: true })).toBeVisible()
  await askToFix(page)

  const assistant = page.getByRole('complementary', { name: 'Assistant' })
  const box = assistant.getByRole('textbox', { name: 'Message the assistant' })
  await expect(box).toHaveValue('Fix this')
  await expect(box).toBeFocused()
  const chips = assistant.getByRole('list', { name: 'Sent with this message' })
  await expect(chips.getByRole('button', { name: `Remove run ${task} from the message` })).toBeVisible()
  // The run attached on its own page is one chip; the payload still names both.
  await expect(chips.getByRole('button', { name: `Remove page ${task} from the message` })).toHaveCount(0)
  await page.screenshot({ path: testInfo.outputPath('fix-this-desktop.png') })

  await assistant.getByRole('button', { name: 'Inspect what is shared' }).click()
  const shared = page.getByRole('dialog', { name: 'What is shared' })
  await expect(shared.getByRole('region', { name: 'Sent with the next message' })).toContainText(run.id)
  await shared.getByRole('button', { name: 'Raw JSON' }).click()
  const raw = JSON.parse(await shared.getByLabel('Raw JSON').innerText()) as unknown
  await page.screenshot({ path: testInfo.outputPath('inspect-desktop.png') })
  await page.keyboard.press('Escape')

  await box.press('Enter')
  await expect.poll(() => prompts.length).toBe(1)
  expect(prompts[0]!.text).toBe('Fix this')
  expect(prompts[0]!.view_context).toEqual(raw)
  expect(prompts[0]!.view_context).toMatchObject({
    route: { name: 'run', run_id: run.id },
    attachments: [{ kind: 'run', run_id: run.id, workspace_id: workspaces[0].id }],
  })
  await expect(box).toHaveValue('')

  await page.setViewportSize({ width: 390, height: 844 })
  const sheet = page.getByRole('dialog', { name: 'Assistant' })
  await sheet.getByRole('button', { name: 'Close assistant' }).click()
  await askToFix(page)
  await expect(sheet.getByRole('textbox', { name: 'Message the assistant' })).toHaveValue('Fix this')
  await expect(sheet.getByRole('list', { name: 'Sent with this message' }).getByRole('button', { name: `Remove run ${task} from the message` })).toBeVisible()
  await expect.poll(async () => (await sheet.boundingBox())?.x).toBe(0)
  await page.screenshot({ path: testInfo.outputPath('fix-this-mobile.png') })
})
