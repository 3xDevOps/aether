// Account sharing through the dashboard: the owner shares in Profile, the
// recipient's launch dialog offers the owner's account only after that, lists
// the agents installed in either home for it, and the run launches in the
// recipient's environment with the owner's login mounted and, since the
// recipient has no installation of their own, the owner's.
//
// The launch dialog never lists the scheduler's `fake` harness, so the run is
// launched by a `claude` shim installed only in the owner's home, which the
// scheduler mounts read-only into the run. On a shared account `claude` also
// needs a login in the owner's home: the spec writes a placeholder
// `~/.claude/.credentials.json` there. Both are Docker volume subpath mounts
// (Docker Engine 26.0 or newer). The shim prints the login it sees, so the
// run's terminal shows whose it is.

import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import type { Locator } from '@playwright/test'

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the environment terminal and the run need a reachable Docker daemon')

/** What the owner's `claude` shim runs: the seed repository's script, then
 * the login at the harness's login path. */
const agentShim = `sh /workspace/agent.sh
printf 'login-seen:%s\\n' "$(cat "$HOME/.claude/.credentials.json")"`
const ownerLogin = '{"claudeAiOauth":{"owner":"alice-e2e"}}'
const task = 'a run on a shared account'

test('a member shares their agent account and a teammate launches on it', async ({
  page,
  browser,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const invite = await aether.invite(alice)
  const bob = await aether.member('bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite, name: 'bob' })

  const displayName = async (member: typeof alice) =>
    (await member.api.rpc<{ member: { display_name: string } }>('server.info')).member
      .display_name
  const aliceName = await displayName(alice)
  const bobName = await displayName(bob)
  const aliceID = await memberID(alice)
  // Only the owner has agents installed; the recipient has none.
  aether.installAgent(aliceID, 'claude', agentShim)
  aether.installAgent(aliceID, 'codex')

  const bobContext = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const bobPage = await bobContext.newPage()
  try {
    const openLaunch = async () => {
      await bobPage.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'New run' }).click()
      const dialog = bobPage.getByRole('dialog', { name: 'New run' })
      await expect(dialog).toBeVisible()
      return dialog
    }
    const chooseAliceAccount = async (dialog: Locator) => {
      await dialog.getByRole('button', { name: /^Options/ }).click()
      await dialog.getByRole('combobox', { name: 'Account', exact: true }).click()
      await bobPage.getByRole('option', { name: `${aliceName} (shared)` }).click()
    }

    // Before the share, Bob has only his own account, so there is no
    // account to choose.
    await bobPage.goto(bob.url)
    let dialog = await openLaunch()
    await expect(dialog.getByRole('radio', { name: /^custom/ })).toBeVisible()
    await expect(dialog.getByRole('button', { name: /^Options/ })).toHaveCount(0)
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toBeHidden()

    // Alice's environment terminal is running when she first shares, so
    // Profile offers to stop it.
    await page.goto(alice.url)
    await page.getByRole('navigation', { name: 'Aether' })
      .getByRole('button', { name: 'Environment', exact: true }).click()
    const dock = page.getByRole('region', { name: 'Environment terminal' })
    await dock.getByRole('button', { name: 'Open', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Save environment' })).toBeVisible({
      timeout: 60_000,
    })

    await page.getByRole('navigation', { name: 'Aether' })
      .getByRole('button', { name: /, (Live|Reconnecting|Offline)$/ }).click()
    await page.getByRole('menuitem', { name: 'Profile' }).click()
    const profile = page.getByRole('dialog', { name: 'Profile' })
    const sharing = profile.getByRole('region', { name: 'Account sharing' })
    await sharing
      .getByRole('listitem')
      .filter({ hasText: bobName })
      .getByRole('button', { name: 'Share account' })
      .click()
    await expect(sharing.getByRole('button', { name: 'Revoke access' })).toBeVisible()
    const notice = sharing.getByRole('status')
    await expect(notice).toContainText(
      `Your Environment was started before you shared, so a Claude Code login written there will not reach ${bobName}'s runs`,
    )
    await notice.getByRole('button', { name: 'Stop environment' }).click()
    const confirm = page.getByRole('alertdialog', { name: 'Stop your environment?' })
    await confirm.getByRole('button', { name: 'Stop environment' }).click()
    await expect(confirm).toBeHidden()
    await expect(notice).toBeHidden()
    await page.keyboard.press('Escape')
    await expect(profile).toBeHidden()

    // After the share, Alice's account is offered with the agents installed
    // in her home, both refused until Alice has a login.
    dialog = await openLaunch()
    await chooseAliceAccount(dialog)
    await expect(dialog.getByRole('status')).toHaveText(
      `${aliceName} is not logged in to Claude Code, Codex, so they cannot launch on this account. ${aliceName} logs in from their own Environment terminal; then open this dialog again.`,
    )
    for (const agent of [/^Claude Code/, /^Codex/]) {
      await expect(dialog.getByRole('radio', { name: agent })).toBeDisabled()
    }

    // The login a vendor flow would leave in Alice's home.
    const login = path.join(aether.server.memberHome(aliceID), '.claude', '.credentials.json')
    mkdirSync(path.dirname(login), { recursive: true })
    writeFileSync(login, `${ownerLogin}\n`, { mode: 0o600 })

    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toBeHidden()
    dialog = await openLaunch()
    await chooseAliceAccount(dialog)
    await expect(dialog.getByRole('radio', { name: /^Claude Code/ })).toBeChecked()
    await expect(dialog.getByRole('status')).toHaveText(
      `${aliceName} is not logged in to Codex, so it cannot launch on this account. ${aliceName} logs in from their own Environment terminal; then open this dialog again.`,
    )
    await dialog.getByLabel('Task').fill(task)
    await dialog.getByRole('button', { name: 'Launch', exact: true }).click()

    await expect(bobPage.getByRole('heading', { name: task, exact: true })).toBeVisible()
    const terminal = bobPage.locator('.xterm-rows:not([data-aether-frozen-view] *)')
    await expect(terminal).toContainText('agent-ready', { timeout: 3 * 60 * 1000 })
    await expect(terminal).toContainText(`login-seen:${ownerLogin}`)

    const facts = bobPage.getByRole('complementary', { name: 'Run details' }).getByRole('region', { name: 'Details' })
    const row = (term: string) =>
      facts
        .getByRole('term')
        .filter({ hasText: new RegExp(`^${term}$`) })
        .locator('..')
        .getByRole('definition')
    await expect(row('Owner')).toContainText(bobName)
    await expect(row('Agent account')).toContainText(aliceName)
  } finally {
    await bobContext.close()
  }
})
