// An agent that reports its own outcome finishes the run, and the run waits
// in Idle until its owner opens it. The agent is a shell fixture that
// calls the real `aether-internal report` over the run's coordination socket.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// The executable starts while the container is still provisioning, so the
// report retries until the socket answers. The idempotency key makes a retry
// after an ambiguous failure safe. The Stop hook ends the turn the way Claude
// Code's own hook does: the finish waits for that, not for a deadline.
const reportSuccess = `i=0
until aether-internal report --outcome success --summary 'outcome review fixture' --idempotency-key outcome-review-fixture >/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 300 ]; then exit 1; fi
  sleep 0.1
done
printf '%s\\n' '{"hook_event_name":"Stop"}' | /opt/aether/aether-server report claude
sleep 600`

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('an agent success report waits in Idle until its owner opens the run', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', reportSuccess)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const task = 'an agent that reports its own success'
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task,
  })

  await page.setViewportSize({ width: 1568, height: 1000 })
  await page.goto(alice.url)
  const card = (column: string) =>
    page.getByRole('region', { name: column }).getByRole('article').filter({ hasText: task })
  await expect(card('Idle')).toBeVisible({ timeout: 180_000 })
  await expect(card('Idle').getByText('The agent reported success; open the run to review it.')).toBeVisible()
  await expect(card('Idle').getByLabel('Done', { exact: true })).toBeVisible()

  await card('Idle').getByRole('button', { name: task, exact: true }).click()
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { outcome_unseen?: boolean } }>('run.get', { run_id: run.id })).run.outcome_unseen ?? false)
    .toBe(false)
  await page.getByRole('navigation', { name: 'Surfaces' }).getByRole('button', { name: 'Board', exact: true }).click()
  await expect(card('Done')).toBeVisible()
  await expect(card('Idle')).toHaveCount(0)
})
