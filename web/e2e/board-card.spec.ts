// Only a real browser shows the hover-revealed action above the card's
// stretched open target, and a real server takes the reply it sends.

import { expect, test } from './fixtures'
import { runCoordCLI } from './harness/coord'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// The container may still be provisioning, so the first report retries
// until the socket answers; the turn then ends once the run is running.
// Only `agent idle` marks a turn end, as the real pi reporter sends it; a
// question waits for one.
const idleAgent = `i=0
until /opt/aether/aether-server report pi --json '{"state":"working"}' >/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 300 ]; then exit 1; fi
  sleep 0.1
done
sleep 2
/opt/aether/aether-server report pi --json '{"state":"waiting","reason":"agent idle"}'
sleep 600`

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('an idle agent stays in Working until it asks, then takes a reply from its Needs you card', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', idleAgent)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const task = 'an agent that stops and waits'
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task,
  })

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)
  const column = (name: string) => page.getByRole('region', { name }).getByRole('article').filter({ hasText: task })
  await expect(column('Working').getByRole('img', { name: 'Waiting' })).toBeVisible({ timeout: 180_000 })
  await expect(column('Needs you')).toHaveCount(0)

  runCoordCLI(run.id, ['report', '--outcome', 'needs-input', '--summary', 'Which suite next?', '--idempotency-key', 'board-question'])
  const card = column('Needs you')
  await expect(card.getByText('Agent asks: Which suite next?')).toBeVisible({ timeout: 30_000 })
  await expect(card.getByRole('img', { name: 'Needs you' })).toBeVisible()

  const reply = card.getByRole('button', { name: 'Reply' })
  await expect(reply).toBeHidden()
  await card.hover()
  await reply.click()
  const composer = page.getByRole('dialog', { name: `Reply to ${task}` })
  await composer.getByRole('textbox', { name: 'Message to the agent' }).fill('carry on with the tests')
  await composer.getByRole('button', { name: 'Send' }).click()

  await expect(composer).toBeHidden()
  await expect(card.getByRole('button', { name: task, exact: true })).toBeFocused()
  await expect(page.getByText(/^Message (sent|queued)$/)).toBeVisible()
  const { messages } = await alice.api.rpc<{ messages: { body: string }[] }>('run.room.list', {
    workspace_id: workspaces[0].id,
    run_id: run.id,
  })
  expect(messages.map((m) => m.body)).toContain('carry on with the tests')

  await card.getByRole('button', { name: task, exact: true }).focus()
  await page.keyboard.press('o')
  await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
})
