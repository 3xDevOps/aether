import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// The container may still be provisioning, so the report retries until the
// socket answers. The finish waits for the Stop hook, not for a deadline.
const reportSuccess = `i=0
until aether-internal report --outcome success --summary 'outcome review fixture' --idempotency-key outcome-review-fixture >/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 300 ]; then exit 1; fi
  sleep 0.1
done
printf '%s\\n' '{"hook_event_name":"Stop"}' | /opt/aether/aether-server report claude
sleep 600`

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('an agent success report waits in Needs you until its owner opens the run', async ({ page, aether }) => {
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
  await expect(card('Needs you')).toBeVisible({ timeout: 180_000 })
  await expect(card('Needs you').getByText('Finished, review the result')).toBeVisible()

  await card('Needs you').hover()
  await expect(card('Needs you').getByRole('button', { name: 'Review', exact: true })).toBeVisible()
  await card('Needs you').click({ position: { x: 16, y: 12 } })
  await expect(page.getByRole('tab', { name: 'Changes', selected: true })).toBeVisible()
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { outcome_unseen?: boolean } }>('run.get', { run_id: run.id })).run.outcome_unseen ?? false)
    .toBe(false)
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'Board', exact: true }).click()
  await expect(card('Finished')).toBeVisible()
  await expect(card('Needs you')).toHaveCount(0)
})
