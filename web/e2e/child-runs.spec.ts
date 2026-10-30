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

test.skip(!dockerReachable(), 'the runs need a reachable Docker daemon')

const launchTimeout = 3 * 60 * 1000
const shots = process.env.AETHER_E2E_SHOTS

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
  const toggle = page.getByRole('checkbox', { name: 'Let my runs start child runs' })
  await expect(toggle).toBeChecked({ timeout: 30_000 })
  const saved = await alice.api.rpc<{ enabled: boolean }>('child.settings', {})
  expect(saved).toEqual({ enabled: true })
  if (shots) await page.screenshot({ path: `${shots}/settings.png` })

  const { run: parent } = await alice.api.rpc<{ run: Run }>('run.launch', {
    workspace_id: workspaces[0].id, task: 'Fix the login redirect', harness: 'claude', mode: 'tui',
  })
  await waitForCoordCLI(parent.id, aether.server.dataDir)
  const options = runCoordCLI<{ allowed: boolean; choices: { harness: string; status: string }[] }>(parent.id, ['child', 'options'])
  expect(options.allowed).toBe(true)
  expect(options.choices).toContainEqual(expect.objectContaining({ harness: 'codex', status: 'ready' }))
  const receipt = runCoordCLI<{ run_id: string; next: string }>(parent.id, [
    'child', 'start', '--harness', 'codex', '--mode', 'standard', '--purpose', 'Adversarial review of the redirect fix',
    '--task', 'Review the last commit for open redirects.', '--idempotency-key', 'review-1',
  ])
  expect(receipt.next).toContain('Do not poll')
  await expect.poll(async () => (await runStatus(alice, receipt.run_id)).status, { timeout: launchTimeout }).toBe('running')
  const child = await runStatus(alice, receipt.run_id)
  expect(child).toMatchObject({ parent_run_id: parent.id, child_purpose: 'Adversarial review of the redirect fix' })

  const parentURL = new URL(alice.url)
  parentURL.searchParams.set('run', parent.id)
  await page.goto(parentURL.toString())
  const section = page.getByRole('region', { name: 'Child runs' })
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
  const lineage = page.getByRole('navigation', { name: 'Run lineage' })
  await expect(lineage.getByText('This child run')).toBeVisible({ timeout: 60_000 })
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
    'child', 'start', '--harness', 'codex', '--mode', 'standard', '--purpose', 'Long-running audit',
    '--task', 'Audit every redirect.', '--idempotency-key', 'audit-1',
  ])
  await expect.poll(async () => (await runStatus(alice, second.run_id)).status, { timeout: launchTimeout }).toBe('running')
  await alice.api.rpc('run.kill', { run_id: parent.id })
  await expect.poll(async () => (await runStatus(alice, second.run_id)).status, { timeout: 60_000 }).toBe('completed')
  expect((await runStatus(alice, second.run_id)).reason).toContain(`stopped by parent run ${parent.id}`)
})
