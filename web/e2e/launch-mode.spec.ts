// A run starts in Enhanced where the agent's Enhanced mode is installed: a
// run.launch that names no mode, as aether run and the MCP connector send,
// starts in it, and New run lists it first, marked Recommended and chosen.
// A Standard picked in New run stays Standard.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the runs need a reachable Docker daemon')

test('a run starts in Enhanced unless Standard is picked', async ({ page, aether }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  aether.installClaudeACPMock(await memberID(alice))
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const workspace = workspaces[0].id
  const { run } = await alice.api.rpc<{ run: { mode: string } }>('run.launch', { workspace_id: workspace, harness: 'claude', task: 'demo' })
  expect(run.mode).toBe('acp')

  const modeOf = async (task: string) => {
    const { runs } = await alice.api.rpc<{ runs: { task: string; mode: string }[] }>('run.list', { workspace_id: workspace })
    return runs.find((r) => r.task === task)?.mode
  }
  const launch = async (task: string, pick?: string) => {
    await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'New run' }).click()
    const dialog = page.getByRole('dialog', { name: 'New run' })
    await expect(dialog.getByRole('radio', { name: /^Claude Code/ })).toBeChecked()
    const modes = dialog.getByRole('radiogroup', { name: 'Mode' }).getByRole('radio')
    await expect(modes.first()).toHaveAccessibleName('Enhanced')
    await expect(modes.first()).toContainText('Recommended')
    await expect(modes.first()).toBeChecked()
    if (pick) await dialog.getByRole('radiogroup', { name: 'Mode' }).getByRole('radio', { name: pick }).click()
    await dialog.getByLabel('Task').fill(task)
    await dialog.getByRole('button', { name: 'Launch', exact: true }).click()
    await expect(page.getByRole('heading', { name: task, exact: true }).first()).toBeVisible()
  }

  await page.goto(alice.url)
  await launch('demo from the dialog')
  expect(await modeOf('demo from the dialog')).toBe('acp')
  await launch('a standard run', 'Standard')
  expect(await modeOf('a standard run')).toBe('tui')
})
