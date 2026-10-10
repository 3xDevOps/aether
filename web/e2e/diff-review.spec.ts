// Review comments on the Changes view against a real server: the agent writes
// a file, the reviewer comments on a line and on a dragged range, and the one
// message they become reaches the agent's terminal.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// Writes the file under review, then echoes each line its terminal receives.
const agent = `sleep 1
printf 'first\\nsecond\\nthird\\n' > review.txt
echo agent-ready
while IFS= read -r line; do
  echo "agent read: $line"
done`

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('comments on the diff reach the agent as one message', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', agent)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'write the file under review',
  })

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${alice.url}&run=${run.id}&view=changes`)
  const file = page.getByRole('region', { name: 'review.txt' })
  const refresh = page.getByRole('button', { name: 'Refresh', exact: true })
  await expect(async () => {
    await refresh.click()
    await expect(file).toBeVisible({ timeout: 2000 })
  }).toPass({ timeout: 3 * 60 * 1000 })
  await expect(page.getByRole('group', { name: 'Review' })).toHaveCount(0)

  const gutter = (code: string) => file.locator('[data-line]').filter({ hasText: code }).locator('[data-slot="line-gutter"]')
  await gutter('second').click()
  await page.getByRole('textbox', { name: 'Comment on review.txt:2' }).fill('say why')
  await page.getByRole('button', { name: 'Comment', exact: true }).click()
  await expect(file.getByRole('article', { name: 'Comment on review.txt:2' })).toContainText('say why')

  await gutter('first').hover()
  await page.mouse.down()
  await gutter('third').hover()
  await page.mouse.up()
  const range = page.getByRole('textbox', { name: 'Comment on review.txt:1-3' })
  await expect(range).toBeFocused()
  await range.fill('tighten these')
  await range.press('ControlOrMeta+Enter')

  const bar = page.getByRole('group', { name: 'Review' })
  await expect(bar).toContainText('2 comments')
  await bar.getByRole('button', { name: 'Send to agent', exact: true }).click()
  // The owner of a run nobody controls sends at once, so this beats the 45
  // seconds a message without the lease would wait.
  await expect(bar.getByRole('status')).toContainText(/^Sent 2 comments to /)
  await expect(file.getByRole('article')).toHaveCount(0)

  const { messages } = await alice.api.rpc<{ messages: { kind: string; body: string; state: string }[] }>(
    'run.room.list', { workspace_id: workspaces[0].id, run_id: run.id },
  )
  expect(messages.filter((message) => message.kind === 'steer_request')).toEqual([expect.objectContaining({
    state: 'sent',
    body: [
      "2 review comments on your changes. Each quotes the diff lines it is about; line numbers are the new file's unless marked removed.",
      '',
      '1. review.txt:1-3',
      '```diff',
      '+first',
      '+second',
      '+third',
      '```',
      'tighten these',
      '',
      '2. review.txt:2',
      '```diff',
      '+second',
      '```',
      'say why',
    ].join('\n'),
  })])

  await bar.getByRole('button', { name: 'Open the session', exact: true }).click()
  await expect(page.getByRole('tab', { name: 'Session', selected: true })).toBeVisible()
  await expect(page.getByRole('log', { name: 'Session' })).toContainText('tighten these')

  await page.getByRole('tab', { name: 'Terminal', exact: true }).click()
  await expect(page.locator('.xterm-rows:not([data-aether-frozen-view] *)')).toContainText('agent read: say why')
})
