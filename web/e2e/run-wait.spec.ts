// Wait conditions through a real server: a run's agent reports waiting
// --until another run finishes, run details lists the pending condition,
// Cancel ends it, killing the target resolves the next one and wakes the
// agent's inbox, and a pull request this server cannot read resolves as
// unreadable. The agents are shell shims; the test reports with
// `aether-internal` in their containers, as an agent does.

import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { runCoordCLI, waitForCoordCLI } from './harness/coord'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the runs need a reachable Docker daemon')

const launchTimeout = 3 * 60 * 1000
const shots = process.env.AETHER_E2E_SHOTS

type Run = { id: string; status: string }

async function shoot(page: Page, name: string, phone: Page | null, url: string) {
  if (!shots) return
  await page.getByRole('region', { name: 'Waiting until' }).screenshot({ path: `${shots}/${name}.png` })
  if (!phone) return
  await phone.goto(url)
  await phone.getByRole('button', { name: 'Show details' }).click()
  await phone.getByRole('region', { name: 'Waiting until' }).scrollIntoViewIfNeeded()
  await phone.screenshot({ path: `${shots}/${name}-mobile.png` })
}

test('a run waits until another run finishes, and a person sees and cancels its conditions', async ({ page, aether, browser }) => {
  test.setTimeout(8 * 60 * 1000)
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('wait-project'), 'wait-project')
  aether.installAgent(await memberID(alice), 'claude', 'printf "agent ready\\n"\nsleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  // The shared test host can be short of the scheduler's memory floor for a while; a launch it refuses for that is retried.
  const launch = async (task: string): Promise<Run> => {
    let run: Run | undefined
    await expect.poll(async () => {
      try {
        run = (await alice.api.rpc<{ run: Run }>('run.launch', { workspace_id: workspaces[0].id, task, harness: 'claude', mode: 'tui' })).run
        return true
      } catch (err) {
        if (String(err).includes('insufficient host memory headroom')) return false
        throw err
      }
    }, { timeout: launchTimeout, intervals: [5_000] }).toBe(true)
    await expect.poll(async () => (await alice.api.rpc<{ run: Run }>('run.get', { run_id: run!.id })).run.status, { timeout: launchTimeout }).toBe('running')
    return run!
  }
  const waiter = await launch('Ship the login fix once the migration lands')
  const migration = await launch('Migrate the session store')
  await waitForCoordCLI(waiter.id, aether.server.dataDir)
  const wait = (key: string, ...until: string[]) => runCoordCLI(waiter.id, ['report', '--outcome', 'waiting',
    ...until.flatMap((u) => ['--until', u]), '--summary', `Waiting until ${until.join(' or ')}`, '--idempotency-key', key])
  wait('wait-1', `run:${migration.id}:finished`)

  const url = new URL(alice.url)
  url.searchParams.set('run', waiter.id)
  await page.goto(url.toString())
  const section = page.getByRole('region', { name: 'Waiting until' })
  const migrationRow = section.getByRole('listitem', { name: `run:${migration.id}:finished` })
  await expect(migrationRow.getByText('Pending', { exact: true })).toBeVisible({ timeout: 60_000 })
  await expect(migrationRow.getByRole('button', { name: 'Migrate the session store' })).toBeVisible()
  await expect(migrationRow.getByText('running', { exact: true })).toBeVisible()
  await expect(section.getByText('The server checks these and wakes the agent when one resolves.')).toBeVisible()
  const phone = shots ? await browser.newPage({ viewport: { width: 393, height: 851 }, isMobile: true, hasTouch: true }) : null
  await shoot(page, 'wait-pending', phone, url.toString())

  await migrationRow.getByRole('button', { name: 'Cancel' }).click()
  await expect(migrationRow.getByText('Cancelled', { exact: true })).toBeVisible({ timeout: 30_000 })
  await expect(migrationRow.getByRole('button', { name: 'Cancel' })).toHaveCount(0)

  wait('wait-2', `run:${migration.id}:finished`)
  await expect(migrationRow.getByText('Pending', { exact: true })).toBeVisible({ timeout: 60_000 })
  await alice.api.rpc('run.kill', { run_id: migration.id })
  await expect(migrationRow.getByText('Abandoned', { exact: true })).toBeVisible({ timeout: 60_000 })
  await expect(migrationRow.getByText('Delivered to the agent')).toBeVisible({ timeout: 60_000 })
  await expect.poll(() => {
    const inbox = runCoordCLI<{ messages: { body: string }[] }>(waiter.id, ['inbox'])
    return inbox.messages.some((m) => m.body.includes(migration.id))
  }, { timeout: 60_000 }).toBe(true)
  await shoot(page, 'wait-resolved', phone, url.toString())

  wait('wait-3', 'pr:acme/api#582:checks')
  const prRow = section.getByRole('listitem', { name: 'pr:acme/api#582:checks' })
  await expect(prRow.getByText('Unreadable', { exact: true })).toBeVisible({ timeout: 60_000 })
  await expect(prRow.getByRole('link', { name: 'acme/api#582' })).toHaveAttribute('href', 'https://github.com/acme/api/pull/582')
  await expect(prRow.getByRole('button', { name: 'Cancel' })).toHaveCount(0)
  await shoot(page, 'wait-failed', phone, url.toString())
  await phone?.close()
})
