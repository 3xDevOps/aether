// Claude Code's Agent calls as Claude's adapter reports them: two subagents
// run in parallel in the background while the main agent keeps working past
// what a reload replays. Their cards stay running across a reload and turn
// done when they report.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

test('background subagents launched before the replayed history stay on the chat across a reload', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installClaudeACPMock(await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0].id, harness: 'claude', mode: 'acp', task: 'demo' })

  await page.goto(`${alice.url}&run=${run.id}`)
  const log = page.getByRole('log', { name: 'Chat' })
  const box = page.getByRole('combobox', { name: 'Message the agent' })
  await expect(log.getByText(/^Finished/).first()).toBeVisible({ timeout: 60_000 })

  const header = page.locator('[data-slot="pane-header"]')
  const inspector = page.getByRole('complementary', { name: 'Subagent' })
  const openAgent = async (id: string, heading: string, state: string) => {
    await page.goto(`${alice.url}&run=${run.id}&agent=${id}-call`)
    await expect(inspector.getByRole('heading', { name: heading })).toBeVisible({ timeout: 30_000 })
    await expect(inspector.getByText(state, { exact: true }).first()).toBeVisible()
  }

  await box.fill('background subagents')
  await box.press('Enter')
  await expect(log.getByText('The subagents are working.')).toBeVisible({ timeout: 30_000 })
  await expect(header.getByText(/2 subagents running/)).toBeVisible()

  // The launches are older than the items a subscribe replays.
  await page.reload()
  await expect(log.getByText('The subagents are working.')).toBeVisible({ timeout: 30_000 })
  await expect(header.getByText(/2 subagents running/)).toBeVisible()
  await openAgent('mock-agent-billing', 'Audit the billing module', 'Running')
  await expect(inspector.getByRole('log', { name: 'Subagent transcript' }).getByText(/src\/billing\.js/).first()).toBeVisible()
  await openAgent('mock-agent-tests', 'Run the test suite', 'Running')

  await box.fill('finish subagents')
  await box.press('Enter')
  await expect(inspector.getByText('Done', { exact: true }).first()).toBeVisible({ timeout: 30_000 })
  await expect(header.getByText(/subagents? running/)).toHaveCount(0)
  await openAgent('mock-agent-billing', 'Audit the billing module', 'Done')
  await expect(inspector.getByRole('log', { name: 'Subagent transcript' }).getByText('Report from mock-agent-billing.')).toBeVisible()
})
