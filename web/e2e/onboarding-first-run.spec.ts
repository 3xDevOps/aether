// The last step: a real run, in a real container, launched from the wizard.
// The picker offers only the agents this account has installed, so the run
// here is launched by an agent this scenario installs: a shim that runs the
// `agent.sh` the seed repository carries, exits cleanly, and completes the
// run with its work committed.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

/** What the installed `claude` shim runs: the seed repository's own script,
 * from the run checkout the scheduler mounts at /workspace. */
const agentShim = 'sh /workspace/agent.sh'

test('the first run completes', async ({ page, aether }) => {
  test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  // The git identity is optional and this scenario is not about it.
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  // Every run forks from the workspace's base branch, so the push is what
  // makes a first run possible at all.
  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  await wizard.repository.continue().click()
  // The install a person does in this step's terminal, done to the same
  // directory: the member's environment home, which agent.list reads and
  // the run container mounts.
  aether.installAgent(await memberID(alice), 'claude', agentShim)
  await wizard.agents.skip().click()

  await wizard.expectStep('First run')
  await wizard.firstRun.launch('claude', 'write the result file')

  // Launching leaves the wizard for the run's terminal, which is where the
  // agent is; the run's own state arrives over the event stream from there.
  await expect(
    page.getByRole('heading', { name: 'write the result file', exact: true }),
  ).toBeVisible()
  const tabs = page.getByRole('tablist', { name: 'Run tabs' })
  await expect(tabs.getByRole('tab', { name: 'Terminal' })).toHaveAttribute(
    'aria-selected',
    'true',
  )

  // The header carries the state on the terminal tab, so nobody has to leave
  // the agent to find out how the run is doing: the fake agent exits
  // cleanly, which now completes the run with its work committed.
  const header = page.locator('header').filter({ hasText: 'write the result file' })
  await expect(header).toContainText('Done', { timeout: 3 * 60 * 1000 })

  // The Overview tab keeps what the header cannot say: why the run stopped.
  await tabs.getByRole('tab', { name: 'Overview' }).click()
  await expect(
    page.getByRole('definition').filter({ hasText: 'agent exited; results committed' }),
  ).toBeVisible()
})

test('with no agent installed the first run sends you back to Agents', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  // Skipping the setup is exactly how a member arrives here with nothing
  // installed, which used to offer `claude` and fail after the launch.
  await wizard.agents.skip().click()

  await wizard.expectStep('First run')
  await expect(wizard.firstRun.section).toContainText('no agent is installed')
  await expect(
    wizard.firstRun.section.getByRole('combobox', { name: 'Agent' }),
  ).toHaveCount(0)
  await expect(wizard.firstRun.button('Launch')).toHaveCount(0)

  await wizard.firstRun.setUpAgent().click()
  await wizard.expectStep('Agents')
  await expect(wizard.agents.setUp('Claude Code')).toBeVisible()
})
