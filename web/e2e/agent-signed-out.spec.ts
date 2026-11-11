// A member's Claude Code login stops signing the agent in. The shell warns
// on every page with the server's own error, Agents says Signed out, and a
// launch is refused with how to sign in again. A good login clears all of it
// without a reload. With a Docker daemon the launch then starts, and the
// agent's own refusal of a turn ("refuse" makes the ACP mock answer
// Authentication required) puts the sign-in steps in the run's reason and
// brings the warning back.

import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

const fileProblem = '~/.claude/.credentials.json: Claude OAuth access token is missing or invalid'
const signInSteps = 'dashboard Agents page, or aether agent login claude'

function credential(accessToken: string): string {
  const expiresAt = Date.now() + 365 * 24 * 60 * 60 * 1000
  return JSON.stringify({ claudeAiOauth: { accessToken, refreshToken: 'fixture-refresh', expiresAt, scopes: ['user:inference'] } }) + '\n'
}

test('a signed-out login warns everywhere, refuses a launch with the way back in, and clears once signed in again', async ({ browser, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  const id = await memberID(alice)
  aether.installClaudeACPMock(id)
  const login = path.join(aether.server.memberHome(id), '.claude', '.credentials.json')
  mkdirSync(path.dirname(login), { recursive: true })
  writeFileSync(login, credential('fixture-access'), { mode: 0o600 })

  const page = await (await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' })).newPage()
  await page.goto(alice.url)
  const nav = page.getByRole('navigation', { name: 'Aether' })
  const alert = page.getByRole('alert').filter({ hasText: 'Sign in on Agents' })
  const recheck = () => page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(nav.getByRole('button', { name: 'New session' })).toBeVisible()
  await expect(alert).toHaveCount(0)

  writeFileSync(login, credential(''), { mode: 0o600 })
  await recheck()
  await expect(alert).toContainText('Claude Code is signed out')
  await expect(alert).toContainText(fileProblem)
  await alert.getByRole('button', { name: 'Sign in on Agents' }).click()
  const row = page.getByRole('listitem').filter({ hasText: 'Claude Code' }).first()
  await expect(row).toContainText('Signed out')
  await expect(row).toContainText(fileProblem)
  await expect(row.getByRole('button', { name: 'Log in Claude Code' })).toBeVisible()

  await nav.getByRole('button', { name: 'New session' }).click()
  const dialog = page.getByRole('dialog', { name: 'New session' })
  await expect(dialog.getByRole('radio', { name: /^Claude Code/ })).toContainText('Signed out')
  await expect(dialog.locator('[data-slot="callout"]').filter({ hasText: 'Claude Code is signed out' })).toContainText(fileProblem)
  await dialog.getByLabel('Task').fill('demo')
  await dialog.getByRole('button', { name: 'Launch', exact: true }).click()
  const refused = dialog.getByRole('alert').filter({ hasText: 'Launch failed' })
  await expect(refused).toContainText(fileProblem)
  await expect(refused).toContainText(`Sign in, then launch again: ${signInSteps}`)

  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Board', exact: true }).click()
  await expect(alert).toContainText(fileProblem)
  writeFileSync(login, credential('fixture-access'), { mode: 0o600 })
  await recheck()
  await expect(alert).toHaveCount(0)
  await nav.getByRole('button', { name: 'New session' }).click()
  await expect(dialog.getByRole('radio', { name: /^Claude Code/ })).toContainText('Ready')
  await dialog.getByLabel('Task').fill('demo')
  await dialog.getByRole('button', { name: 'Launch', exact: true }).click()
  if (!dockerReachable()) {
    // Without Docker the launch gets past the login check and fails later.
    await expect(refused).toContainText('scheduler:')
    await expect(refused).not.toContainText('signed out')
    return
  }
  await expect(dialog).toBeHidden()
  const log = page.getByRole('log', { name: 'Chat' })
  await expect(log.getByText(/^Finished/).first()).toBeVisible({ timeout: 60_000 })

  const { runs } = await alice.api.rpc<{ runs: { id: string }[] }>('run.list')
  const reason = async () => (await alice.api.rpc<{ run: { reason?: string } }>('run.get', { run_id: runs[0]!.id })).run.reason ?? ''
  const box = page.getByRole('combobox', { name: 'Message the agent' })
  await box.fill('refuse')
  await box.press('Enter')
  await expect.poll(reason, { timeout: 60_000 }).toMatch(/^enhanced turn failed: .*Authentication required/)
  expect(await reason()).toContain(`Refused again? Sign in: ${signInSteps}`)
  await expect(page.getByText(/Turn failed: .*Authentication required/).first()).toBeVisible()
  const callout = page.locator('[data-slot="callout"]').filter({ hasText: "Claude Code's sign-in was refused" })
  await expect(callout.getByRole('button', { name: 'Sign in on Agents' })).toBeVisible()
  await expect(alert).toHaveCount(0)
  await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('button', { name: 'Board', exact: true }).click()
  await expect(alert).toContainText("Claude Code's sign-in was refused")
  await expect(alert).toContainText('the agent refused: Authentication required')
})
