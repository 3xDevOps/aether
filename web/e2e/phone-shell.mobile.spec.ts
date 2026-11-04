// The phone shell against a real server: the tab bar at the narrowest and
// widest common phones, Needs you as the landing page with its count, and
// an agent's permission denied from its card (approving one opens the
// session, where its change is shown) and a question answered from its card.

import { runCoordCLI } from './harness/coord'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { expect, test } from './mobile'

const overflowX = (page: import('@playwright/test').Page) =>
  page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)

for (const width of [360, 430]) {
  test(`the tab bar fits a ${width}px phone and Needs you is home`, async ({ page, aether }) => {
    await page.setViewportSize({ width, height: 800 })
    const alice = await aether.member('alice')
    const repo = await aether.seedRepo('project')
    await seedWorkspace(alice, aether.server.addr, repo)
    await page.goto(alice.url)

    const tabs = page.getByRole('navigation', { name: 'Main navigation' })
    await expect(tabs).toBeInViewport({ ratio: 1 })
    const needsYou = tabs.getByRole('button', { name: /^Needs you/ })
    await expect(needsYou).toHaveAttribute('aria-current', 'page')
    await expect(page.getByRole('banner')).toContainText('Needs you')
    await expect(page.getByRole('heading', { name: 'Nothing needs you' })).toBeVisible()
    for (const name of [/^Needs you/, 'Sessions', 'New session', 'More']) {
      const box = await tabs.getByRole('button', { name }).boundingBox()
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44)
      expect(box?.width ?? 0).toBeGreaterThanOrEqual(44)
    }
    expect(await overflowX(page)).toBe(0)

    await tabs.getByRole('button', { name: 'Sessions' }).tap()
    await expect(page.getByRole('main').getByRole('region', { name: 'Sessions' })).toBeVisible()
    expect(await overflowX(page)).toBe(0)

    await tabs.getByRole('button', { name: 'More' }).tap()
    const more = page.getByRole('dialog', { name: 'More' })
    await more.getByRole('button', { name: 'Settings' }).tap()
    await expect(more).toBeHidden()
    await expect(page.getByRole('banner')).toContainText('Settings')
    expect(await overflowX(page)).toBe(0)
  })
}

// The container may still be provisioning, so the first report retries
// until the socket answers; then the turn ends, as the pi reporter says it.
const idleAgent = `i=0
until /opt/aether/aether-server report pi --json '{"state":"working"}' >/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 300 ]; then exit 1; fi
  sleep 0.1
done
sleep 2
/opt/aether/aether-server report pi --json '{"state":"waiting","reason":"agent idle"}'
sleep 600`

test('Needs you denies a permission and replies to a question from their cards', async ({ page, aether }) => {
  test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const id = await memberID(alice)
  await aether.installACPMock(alice, id)
  aether.installAgent(id, 'claude', idleAgent)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const launch = (params: Record<string, unknown>) =>
    alice.api.rpc<{ run: { id: string } }>('run.launch', { workspace_id: workspaces[0].id, ...params })
  const asker = await launch({ harness: 'mock', mode: 'acp', task: 'ask permission' })
  const idle = await launch({ harness: 'claude', task: 'an agent that stops and asks' })

  await page.goto(alice.url)
  const cards = page.getByRole('list', { name: 'Needs you' })
  const permission = cards.getByRole('group').filter({ hasText: 'Permission requested' })
  await expect(permission.getByRole('button', { name: 'Review' })).toBeVisible({ timeout: 180_000 })
  await expect(permission.getByRole('button', { name: 'Approve' })).toHaveCount(0)
  await expect(permission.getByRole('button', { name: /always/i })).toHaveCount(0)

  await expect.poll(async () => {
    const status = await alice.api.rpc<{ run: { state?: string } }>('run.get', { run_id: idle.run.id })
    return status.run.state
  }, { timeout: 180_000 }).toBe('waiting')
  runCoordCLI(idle.run.id, ['report', '--outcome', 'needs-input', '--summary', 'Which suite next?', '--idempotency-key', 'phone-question'])
  const question = cards.getByRole('group').filter({ hasText: 'Agent asks: Which suite next?' })
  await expect(question).toBeVisible({ timeout: 30_000 })

  const tabs = page.getByRole('navigation', { name: 'Main navigation' })
  await expect(tabs.getByRole('button', { name: 'Needs you, 2' })).toBeVisible()
  // Requests rank before what only needs the agent's own question answered.
  await expect(cards.getByRole('group').first()).toContainText('Permission requested')

  const deny = permission.getByRole('button', { name: 'Deny' })
  expect((await deny.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(44)
  await deny.tap()
  await expect(permission).toHaveCount(0, { timeout: 30_000 })
  await expect(tabs.getByRole('button', { name: 'Needs you, 1' })).toBeVisible()
  await expect.poll(async () => {
    const history = await alice.api.rpc<{ frames: { item?: { text?: string } }[] }>('run.acp.history', { run_id: asker.run.id })
    return JSON.stringify(history.frames)
  }, { timeout: 30_000 }).toContain('permission: reject')

  await question.getByRole('button', { name: 'Reply…' }).tap()
  await question.getByRole('textbox', { name: /^Reply to / }).fill('carry on with the tests')
  await question.getByRole('button', { name: 'Send', exact: true }).tap()
  await expect(page.getByText(/^Reply (sent|queued)$/)).toBeVisible()
  const { messages } = await alice.api.rpc<{ messages: { body: string }[] }>('run.room.list', {
    workspace_id: workspaces[0].id,
    run_id: idle.run.id,
  })
  expect(messages.map((m) => m.body)).toContain('carry on with the tests')

  await question.getByRole('button', { name: 'an agent that stops and asks' }).tap()
  await expect(page.getByRole('heading', { name: 'an agent that stops and asks', exact: true })).toBeVisible()
})
