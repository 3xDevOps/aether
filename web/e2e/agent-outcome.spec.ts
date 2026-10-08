import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// The container may still be provisioning, so the report retries until the
// socket answers. The outcome becomes reviewable at the Stop hook.
const reportSuccess = `i=0
until aether-internal report --outcome success --summary 'outcome review fixture' --idempotency-key outcome-review-fixture >/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 300 ]; then exit 1; fi
  sleep 0.1
done
printf '%s\\n' '{"hook_event_name":"Stop"}' | /opt/aether/aether-server report claude
while IFS= read -r line; do
  printf '%s\\n' '{"hook_event_name":"UserPromptSubmit"}' | /opt/aether/aether-server report claude
  echo "follow-up received: $line"
done`

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('completion stays reviewable and the same run accepts a follow-up', async ({ page, aether }) => {
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

  await card('Needs you').hover()
  await expect(card('Needs you').getByRole('button', { name: 'Review', exact: true })).toBeVisible()
  await card('Needs you').click({ position: { x: 16, y: 12 } })
  await expect(page.getByRole('tab', { name: 'Changes', selected: true })).toBeVisible()
  await expect
    .poll(async () => (await alice.api.rpc<{ run: { outcome_unseen?: boolean } }>('run.get', { run_id: run.id })).run.outcome_unseen ?? false)
    .toBe(false)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Board', exact: true }).click()
  await expect(card('Finished')).toBeVisible()
  await expect(card('Needs you')).toHaveCount(0)
  const before = (await alice.api.rpc<{ run: { status: string; started_at: string; finished_at: string | null; container_retained_until?: string } }>('run.get', { run_id: run.id })).run
  expect(before.status).toBe('needs-attention')
  expect(before.finished_at).toBeNull()
  expect(before.container_retained_until).toBeUndefined()
  await card('Finished').click({ position: { x: 16, y: 12 } })
  await page.getByRole('tab', { name: 'Session', exact: true }).click()
  await page.getByRole('textbox', { name: 'Message the agent' }).fill('please check the retry path')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect.poll(async () => {
    const { run: continued } = await alice.api.rpc<{ run: { status: string; started_at: string } }>('run.get', { run_id: run.id })
    expect(continued.started_at).toBe(before.started_at)
    return continued.status
  }).toBe('running')
})
