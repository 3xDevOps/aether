import { expect, test, type Aether } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// The run may still be provisioning, which includes up to 25 seconds of the
// pre-launch harness update, so the report retries for as long as the test
// waits for the card. The outcome becomes reviewable at the Stop hook.
const reportSuccess = `end=$(($(date +%s) + 180))
until aether-internal report --outcome success --summary 'outcome review fixture' --idempotency-key outcome-review-fixture >/dev/null; do
  if [ "$(date +%s)" -ge "$end" ]; then exit 1; fi
  sleep 0.1
done
printf '%s\\n' '{"hook_event_name":"Stop"}' | /opt/aether/aether-server report claude
while IFS= read -r line; do
  printf '%s\\n' '{"hook_event_name":"UserPromptSubmit"}' | /opt/aether/aether-server report claude
  echo "follow-up received: $line"
done`

test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')

async function launch(aether: Aether, task: string) {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', reportSuccess)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task,
  })
  return { alice, run }
}

interface Reviewed {
  status: string
  started_at: string
  finished_at: string | null
  container_retained_until?: string
  outcome_unseen?: boolean
  review?: string
}

test('a reported success waits in Needs you until answered, and the same run accepts a follow-up', async ({ page, aether }) => {
  const task = 'an agent that reports its own success'
  const { alice, run } = await launch(aether, task)
  const read = async () => (await alice.api.rpc<{ run: Reviewed }>('run.get', { run_id: run.id })).run

  await page.setViewportSize({ width: 1568, height: 1000 })
  await page.goto(alice.url)
  const card = (column: string) =>
    page.getByRole('region', { name: column }).getByRole('article').filter({ hasText: task })
  const board = () => page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Board', exact: true }).click()
  await expect(card('Needs you').getByRole('img', { name: 'Ready for review' })).toBeVisible({ timeout: 180_000 })
  expect((await read()).review).toBe('ready')

  // Opening the run marks the outcome seen but leaves the question open.
  await card('Needs you').click({ position: { x: 16, y: 12 } })
  await expect.poll(async () => (await read()).outcome_unseen ?? false).toBe(false)
  const prompt = page.getByRole('region', { name: 'Completion' })
  await expect(prompt).toContainText('Your agent reports this task is finished. Mark it complete?')
  await expect(prompt).toContainText('outcome review fixture')
  await board()
  await expect(card('Needs you')).toBeVisible()

  await card('Needs you').click({ position: { x: 16, y: 12 } })
  await prompt.getByRole('button', { name: 'Not yet' }).click()
  await expect(prompt).toHaveCount(0)
  await expect.poll(async () => (await read()).review).toBe('later')
  await board()
  await expect(card('Needs you')).toHaveCount(0)
  await expect(card('Finished').getByRole('img', { name: 'Ready for review' })).toBeVisible()

  const before = await read()
  expect(before.status).toBe('needs-attention')
  expect(before.finished_at).toBeNull()
  expect(before.container_retained_until).toBeUndefined()
  await card('Finished').click({ position: { x: 16, y: 12 } })
  await page.getByRole('tab', { name: 'Chat', exact: true }).click()
  await page.getByRole('textbox', { name: 'Message the agent' }).fill('please check the retry path')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect.poll(async () => {
    const continued = await read()
    expect(continued.started_at).toBe(before.started_at)
    return continued.status
  }).toBe('running')
})

test('Mark complete closes a reported success as completed', async ({ page, aether }) => {
  const task = 'an agent whose success is accepted'
  const { alice, run } = await launch(aether, task)
  const read = async () => (await alice.api.rpc<{ run: Reviewed }>('run.get', { run_id: run.id })).run

  await page.setViewportSize({ width: 1568, height: 1000 })
  await page.goto(alice.url)
  const card = (column: string) =>
    page.getByRole('region', { name: column }).getByRole('article').filter({ hasText: task })
  await expect(card('Needs you')).toBeVisible({ timeout: 180_000 })
  await card('Needs you').click({ position: { x: 16, y: 12 } })
  const prompt = page.getByRole('region', { name: 'Completion' })
  await prompt.getByRole('button', { name: 'Mark complete' }).click()
  await expect(prompt).toHaveCount(0)
  await expect(page.getByRole('status').filter({ hasText: 'Completed' })).toBeVisible({ timeout: 60_000 })
  await expect.poll(async () => {
    const done = await read()
    return [done.status, done.review]
  }, { timeout: 60_000 }).toEqual(['completed', 'accepted'])
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Board', exact: true }).click()
  await expect(card('Needs you')).toHaveCount(0)
  await expect(card('Finished').getByRole('img', { name: 'Completed' })).toBeVisible()
})

test('a completed run inside its grace relaunches from the composer and its grace restarts at the next close', async ({ page, aether }) => {
  const task = 'an agent that is relaunched after completion'
  const { alice, run } = await launch(aether, task)
  const read = async () => (await alice.api.rpc<{ run: Reviewed }>('run.get', { run_id: run.id })).run

  await page.setViewportSize({ width: 1568, height: 1000 })
  await page.goto(alice.url)
  const card = (column: string) =>
    page.getByRole('region', { name: column }).getByRole('article').filter({ hasText: task })
  await expect(card('Needs you')).toBeVisible({ timeout: 180_000 })
  await card('Needs you').click({ position: { x: 16, y: 12 } })
  await page.getByRole('region', { name: 'Completion' }).getByRole('button', { name: 'Mark complete' }).click()
  await expect.poll(async () => (await read()).status, { timeout: 60_000 }).toBe('completed')
  const closed = await read()
  expect(closed.container_retained_until).toBeTruthy()

  await page.getByRole('tab', { name: 'Chat', exact: true }).click()
  await page.getByRole('button', { name: 'Relaunch', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Message the agent' })).toBeVisible({ timeout: 60_000 })
  await expect.poll(async () => (await read()).status).toBe('running')
  const live = await read()
  expect(live.finished_at).toBeNull()
  expect(live.container_retained_until).toBeUndefined()
})
