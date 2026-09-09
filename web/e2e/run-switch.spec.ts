// Opening one run after another on the Terminal tab. The run-detail view is
// reused across the switch, so the pane has to be cleared by the switch
// itself: a run whose attach is never answered - a finished run with no
// recorded terminal - would otherwise keep showing the run before it.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

/** What the installed `claude` shim runs: the seed repository's own script,
 * from the run checkout the scheduler mounts at /workspace. */
const agentShim = 'sh /workspace/agent.sh'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a second run never shows the first run output', async ({ page, aether }) => {
  // Installed before the first navigation, the only point where the page's
  // WebSocket can be replaced. Every attach reaches the server except the one
  // named here, which is left unanswered the way the server's refusal of a
  // transcript-less run leaves the pane.
  let unanswered = ''
  await page.routeWebSocket(/\/ws\/attach\//, (ws) => {
    const runID = new URL(ws.url()).pathname.split('/').pop() ?? ''
    if (runID !== unanswered) ws.connectToServer()
  })

  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  await wizard.repository.continue().click()
  // The picker offers only the agents this account has installed.
  aether.installAgent(await memberID(alice), 'claude', agentShim)
  await wizard.agents.skip().click()
  await wizard.expectStep('First run')
  await wizard.firstRun.launch('claude', 'write the result file')

  const pane = page.locator('.xterm-rows')
  await expect(pane).toContainText('agent-ready', { timeout: 3 * 60 * 1000 })

  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'the second run',
  })
  unanswered = run.id

  const sidebar = page.getByRole('complementary')
  await sidebar.getByRole('button', { name: /the second run/ }).click()
  await expect(
    page.getByRole('heading', { name: 'the second run', exact: true }),
  ).toBeVisible()
  // The attach is still unanswered, so nothing has written to this pane yet.
  await expect(page.getByText('Connecting')).toBeVisible()
  await expect(pane).not.toContainText('agent-ready')
})
