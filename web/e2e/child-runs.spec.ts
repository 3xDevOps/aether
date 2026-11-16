// Child runs through the dashboard and a real server: they are on by
// default, the parent run's agent starts a Codex child over the
// coordination CLI in its own container, the child shows under its parent
// with its purpose and account, its report reaches the parent's inbox, and
// killing the parent stops the child.
//
// The agents are shell shims that wait; the test runs `aether-internal` in
// their containers, the same socket and admission path an agent uses.

import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { expect, test } from './fixtures'
import { runCoordCLI, waitForCoordCLI } from './harness/coord'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the sessions need a reachable Docker daemon')
test.use({ colorScheme: 'dark' })

const launchTimeout = 3 * 60 * 1000
const shots = process.env.AETHER_E2E_SHOTS

// A report shows once the agent's turn has ended, so this agent works for two seconds and then idles.
const idleAgent = `i=0
until /opt/aether/aether-server report pi --json '{"state":"working"}' >/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 300 ]; then exit 1; fi
  sleep 0.1
done
sleep 2
/opt/aether/aether-server report pi --json '{"state":"waiting","reason":"agent idle"}'
sleep 600`

type Run = { id: string; status: string; reason?: string; parent_run_id?: string; child_purpose?: string }

// A queued child has no run yet, so run.get answers not found until it starts.
async function runStatus(member: { api: { rpc: <T>(m: string, p?: unknown) => Promise<T> } }, id: string): Promise<Run> {
  try {
    return (await member.api.rpc<{ run: Run }>('run.get', { run_id: id })).run
  } catch (err) {
    if (String(err).includes('not found')) return { id, status: 'queued' }
    throw err
  }
}

test('an agent starts a child run that its owner sees, follows and stops', async ({ page, aether, browser }) => {
  test.setTimeout(8 * 60 * 1000)
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('child-runs-project')
  await seedWorkspace(alice, aether.server.addr, repo, 'child-runs-project')
  const aliceID = await memberID(alice)
  aether.installAgent(aliceID, 'claude', 'printf "parent agent ready\\n"\nsleep 600')
  aether.installAgent(aliceID, 'codex', 'printf "reviewer ready\\n"\nsleep 600')
  const claudeHome = path.join(aether.server.memberHome(aliceID), '.claude')
  mkdirSync(claudeHome, { recursive: true })
  writeFileSync(path.join(claudeHome, '.credentials.json'), '{}\n')
  aether.giveCodexLogin(aliceID)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')

  const settings = new URL(alice.url)
  settings.searchParams.set('page', 'settings')
  settings.searchParams.set('id', 'children')
  await page.goto(settings.toString())
  const toggle = page.getByRole('checkbox', { name: 'Let my sessions start subsessions' })
  await expect(toggle).toBeChecked({ timeout: 30_000 })
  const saved = await alice.api.rpc<{ enabled: boolean; off: unknown[] }>('child.settings', {})
  expect(saved).toMatchObject({ enabled: true, off: [] })
  if (shots) await page.screenshot({ path: `${shots}/settings.png` })

  const { run: parent } = await alice.api.rpc<{ run: Run }>('run.launch', {
    workspace_id: workspaces[0].id, task: 'Fix the login redirect', harness: 'claude', mode: 'tui',
  })
  await waitForCoordCLI(parent.id, aether.server.dataDir)
  const options = runCoordCLI<{ allowed: boolean; choices: { harness: string; status: string }[] }>(parent.id, ['subsession', 'options'])
  expect(options.allowed).toBe(true)
  expect(options.choices).toContainEqual(expect.objectContaining({ harness: 'codex', status: 'ready' }))
  const receipt = runCoordCLI<{ run_id: string; next: string }>(parent.id, [
    'subsession', 'start', '--harness', 'codex', '--mode', 'standard', '--purpose', 'Adversarial review of the redirect fix',
    '--task', 'Review the last commit for open redirects.', '--idempotency-key', 'review-1',
  ])
  expect(receipt.next).toContain('Do not poll')
  await expect.poll(async () => (await runStatus(alice, receipt.run_id)).status, { timeout: launchTimeout }).toBe('running')
  const child = await runStatus(alice, receipt.run_id)
  expect(child).toMatchObject({ parent_run_id: parent.id, child_purpose: 'Adversarial review of the redirect fix' })

  const parentURL = new URL(alice.url)
  parentURL.searchParams.set('run', parent.id)
  await page.goto(parentURL.toString())
  const section = page.getByRole('region', { name: 'Subsessions' })
  await expect(section.getByText('Adversarial review of the redirect fix')).toBeVisible({ timeout: 60_000 })
  await expect(section.getByText(/^Working · codex · On \S+'s account · Cost not reported$/)).toBeVisible()
  if (shots) await page.screenshot({ path: `${shots}/parent.png`, fullPage: true })

  await waitForCoordCLI(receipt.run_id, aether.server.dataDir)
  runCoordCLI(receipt.run_id, ['report', '--outcome', 'success', '--summary', 'No open redirect; one test missing.', '--idempotency-key', 'verdict'])
  await expect.poll(() => {
    const inbox = runCoordCLI<{ messages: { from_run_id: string; body: string }[] }>(parent.id, ['inbox'])
    return inbox.messages.find((m) => m.from_run_id === receipt.run_id)?.body ?? ''
  }, { timeout: 60_000 }).toBe('No open redirect; one test missing.')
  await expect(section.getByText('No open redirect; one test missing.')).toBeVisible({ timeout: 60_000 })

  const childURL = new URL(alice.url)
  childURL.searchParams.set('run', receipt.run_id)
  await page.goto(childURL.toString())
  const lineage = page.getByRole('navigation', { name: 'Session lineage' })
  await expect(lineage.getByText('This subsession')).toBeVisible({ timeout: 60_000 })
  if (shots) await page.screenshot({ path: `${shots}/child.png` })
  if (shots) {
    const phone = await browser.newPage({ viewport: { width: 393, height: 851 }, isMobile: true, hasTouch: true })
    await phone.goto(parentURL.toString())
    await phone.getByRole('button', { name: 'Show details' }).click()
    await expect(phone.getByText('Adversarial review of the redirect fix').first()).toBeVisible({ timeout: 60_000 })
    await phone.screenshot({ path: `${shots}/parent-mobile.png`, fullPage: true })
    await phone.close()
  }

  const second = runCoordCLI<{ run_id: string }>(parent.id, [
    'subsession', 'start', '--harness', 'codex', '--mode', 'standard', '--purpose', 'Long-running audit',
    '--task', 'Audit every redirect.', '--idempotency-key', 'audit-1',
  ])
  await expect.poll(async () => (await runStatus(alice, second.run_id)).status, { timeout: launchTimeout }).toBe('running')
  await alice.api.rpc('run.kill', { run_id: parent.id })
  await expect.poll(async () => (await runStatus(alice, second.run_id)).status, { timeout: 60_000 }).toBe('completed')
  expect((await runStatus(alice, second.run_id)).reason).toContain(`stopped by parent session ${parent.id}`)
})

test('the sidebar folds a parent session’s subsessions and leaves out the finished ones', async ({ page, aether }) => {
  test.setTimeout(8 * 60 * 1000)
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('sidebar-subsessions-project')
  await seedWorkspace(alice, aether.server.addr, repo, 'sidebar-subsessions-project')
  const aliceID = await memberID(alice)
  aether.installAgent(aliceID, 'claude', idleAgent)
  // The children are Claude sessions: a Codex shim has no app-server, and its failure replaces a reported state.
  const claudeHome = path.join(aether.server.memberHome(aliceID), '.claude')
  mkdirSync(claudeHome, { recursive: true })
  writeFileSync(path.join(claudeHome, '.credentials.json'), '{}\n')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run: parent } = await alice.api.rpc<{ run: Run }>('run.launch', {
    workspace_id: workspaces[0].id, task: 'Coordinate the sidebar check', harness: 'claude', mode: 'tui',
  })
  await waitForCoordCLI(parent.id, aether.server.dataDir)
  const child = runCoordCLI<{ run_id: string }>(parent.id, [
    'subsession', 'start', '--harness', 'claude', '--mode', 'standard', '--purpose', 'Sidebar fold check',
    '--task', 'Wait for the parent to stop this check.', '--idempotency-key', 'sidebar-fold-check',
  ])
  await expect.poll(async () => (await runStatus(alice, child.run_id)).status, { timeout: launchTimeout }).toBe('running')

  const parentURL = new URL(alice.url)
  parentURL.searchParams.set('run', parent.id)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(parentURL.toString())
  const sidebar = page.getByRole('navigation', { name: 'Cloudfield' }).getByRole('region', { name: 'Sessions' })
  const fold = () => sidebar.getByRole('button', { name: /^(Collapse|Expand) 1 subsession$/ })
  const childRow = sidebar.getByRole('button', { name: /Sidebar fold check/ })
  await expect(fold()).toHaveAttribute('aria-expanded', 'true')
  await expect(childRow).toBeVisible()
  if (shots) await page.screenshot({ path: `${shots}/sidebar-subsessions-expanded.png` })
  await fold().click()
  await expect(fold()).toHaveAttribute('aria-expanded', 'false')
  await expect(childRow).toBeHidden()
  if (shots) await page.screenshot({ path: `${shots}/sidebar-subsessions-folded.png` })
  await fold().click()
  await expect(childRow).toBeVisible()

  const start = (purpose: string, key: string) => runCoordCLI<{ run_id: string }>(parent.id, [
    'subsession', 'start', '--harness', 'claude', '--mode', 'standard', '--purpose', purpose,
    '--task', 'Wait for the parent to stop this check.', '--idempotency-key', key,
  ])
  const failing = start('Sidebar failing check', 'sidebar-failing-check')
  const asking = start('Sidebar asking check', 'sidebar-asking-check')
  for (const started of [failing, asking]) {
    await expect.poll(async () => (await runStatus(alice, started.run_id)).status, { timeout: launchTimeout }).toBe('running')
    await waitForCoordCLI(started.run_id, aether.server.dataDir)
  }
  await waitForCoordCLI(child.run_id, aether.server.dataDir)
  for (const purpose of ['Sidebar fold check', 'Sidebar failing check', 'Sidebar asking check']) {
    await expect(sidebar.getByRole('button', { name: new RegExp(`^Turn ended · ${purpose}`) })).toBeVisible({ timeout: 60_000 })
  }
  runCoordCLI(child.run_id, ['report', '--outcome', 'success', '--summary', 'The fold works.', '--idempotency-key', 'fold-verdict'])
  runCoordCLI(failing.run_id, ['report', '--outcome', 'failure', '--summary', 'The check could not run.', '--idempotency-key', 'failing-verdict'])
  runCoordCLI(asking.run_id, ['report', '--outcome', 'needs-input', '--summary', 'Which width next?', '--idempotency-key', 'asking-question'])

  // A finished subsession leaves the list; one that failed or asks stays, and the parent still lists all three.
  const finished = sidebar.getByRole('button', { name: '1 finished', exact: true })
  await expect(finished).toBeVisible({ timeout: 60_000 })
  await expect(childRow).toBeHidden()
  await expect(sidebar.getByRole('button', { name: /Sidebar failing check/ })).toBeVisible()
  await expect(sidebar.getByRole('button', { name: /Sidebar asking check/ })).toBeVisible()
  const section = page.getByRole('region', { name: 'Subsessions' })
  for (const purpose of ['Sidebar fold check', 'Sidebar failing check', 'Sidebar asking check']) {
    await expect(section.getByText(purpose)).toBeVisible({ timeout: 60_000 })
  }
  await expect(section.getByText('The fold works.')).toBeVisible()
  if (shots) await page.screenshot({ path: `${shots}/sidebar-subsessions-finished.png` })
  await finished.click()
  await expect(childRow).toBeVisible()
  await sidebar.getByRole('button', { name: 'Hide finished', exact: true }).click()
  await expect(childRow).toBeHidden()

  await page.setViewportSize({ width: 768, height: 900 })
  if (shots) await page.screenshot({ path: `${shots}/sidebar-subsessions-narrow.png` })
  await alice.api.rpc('run.kill', { run_id: parent.id })
})
