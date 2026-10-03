// Account sharing through the dashboard: the owner shares on Members, the
// recipient's launch dialog offers the owner's account only after that, lists
// the recipient's own installed agents for it, and the run launches in the
// recipient's environment with the owner's login mounted.
//
// The launch dialog never lists the scheduler's `fake` harness, so the run is
// launched by a `claude` shim installed in the recipient's home. On a shared
// account `claude` needs a login in the owner's home: the spec writes a
// placeholder `~/.claude/.credentials.json` there, which the scheduler mounts
// into the run as a Docker volume subpath (Docker Engine 26.0 or newer). The
// shim prints the login it sees, so the run's terminal shows whose it is.

import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'the environment terminal and the run need a reachable Docker daemon')

/** What the recipient's `claude` shim runs: the seed repository's script,
 * then the login at the harness's login path. */
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
  // The recipient has claude installed; only the owner has codex.
  aether.installAgent(await memberID(bob), 'claude', agentShim)
  aether.installAgent(aliceID, 'codex')

  const bobContext = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const bobPage = await bobContext.newPage()
  try {
    const openLaunch = async () => {
      await bobPage.getByRole('banner', { name: 'Aether' }).getByRole('button', { name: 'New run' }).click()
      const dialog = bobPage.getByRole('dialog', { name: 'Launch a run' })
      await expect(dialog).toBeVisible()
      return dialog
    }

    // Before the share, Bob's Account picker offers only his own account.
    await bobPage.goto(bob.url)
    let dialog = await openLaunch()
    await dialog.getByRole('combobox', { name: 'Account', exact: true }).click()
    await expect(bobPage.getByRole('option', { name: `${bobName} (you)` })).toBeVisible()
    await expect(bobPage.getByRole('option', { name: `${aliceName} (shared)` })).toHaveCount(0)
    await bobPage.keyboard.press('Escape')
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toBeHidden()

    // Alice's environment terminal is running when she first shares, so
    // Members offers to stop it.
    await page.goto(alice.url)
    const dock = page.getByRole('region', { name: 'Terminal dock' })
    await dock.getByRole('button', { name: 'Expand terminal dock' }).click()
    await dock.getByRole('button', { name: 'Open', exact: true }).click()
    await expect(dock.getByRole('button', { name: 'Save environment' })).toBeVisible({
      timeout: 60_000,
    })

    await page.getByRole('navigation', { name: 'Surfaces' })
      .getByRole('button', { name: /^Admin(?:,|$)/ }).click()
    await page.getByRole('menuitem', { name: 'Members', exact: true }).click()
    const sharing = page.getByRole('region', { name: 'Account sharing' })
    await sharing
      .getByRole('listitem')
      .filter({ hasText: bobName })
      .getByRole('button', { name: 'Share account' })
      .click()
    await expect(sharing.getByRole('button', { name: 'Revoke access' })).toBeVisible()
    const notice = sharing.getByRole('status')
    await expect(notice).toContainText(
      `Your environment terminal was started before you shared, so a Claude Code login written there will not reach ${bobName}'s runs`,
    )
    await notice.getByRole('button', { name: 'Stop environment' }).click()
    const confirm = page.getByRole('alertdialog', { name: 'Stop your environment?' })
    await confirm.getByRole('button', { name: 'Stop environment' }).click()
    await expect(confirm).toBeHidden()
    await expect(notice).toBeHidden()

    // After the share, Alice's account is offered, with Bob's own agents:
    // his claude, refused until Alice has a login, and not Alice's codex.
    dialog = await openLaunch()
    const account = dialog.getByRole('combobox', { name: 'Account', exact: true })
    await account.click()
    await bobPage.getByRole('option', { name: `${aliceName} (shared)` }).click()
    await expect(dialog.getByRole('status')).toHaveText(
      `${aliceName} is not logged in to claude, so it cannot launch on this account. ${aliceName} logs in from the terminal dock on their own Board; then press Refresh agents.`,
    )
    await dialog.getByRole('combobox', { name: 'Agent', exact: true }).click()
    await expect(bobPage.getByRole('option', { name: 'claude (not logged in)' })).toHaveAttribute(
      'aria-disabled',
      'true',
    )
    await expect(bobPage.getByRole('option', { name: /^codex/ })).toHaveCount(0)
    await bobPage.keyboard.press('Escape')

    // The login a vendor flow would leave in Alice's home.
    const login = path.join(aether.server.memberHome(aliceID), '.claude', '.credentials.json')
    mkdirSync(path.dirname(login), { recursive: true })
    writeFileSync(login, `${ownerLogin}\n`, { mode: 0o600 })

    await dialog.getByRole('button', { name: 'Refresh agents' }).click()
    await expect(dialog.getByRole('combobox', { name: 'Agent', exact: true })).toHaveText('claude')
    await expect(dialog.getByRole('status')).toHaveCount(0)
    await dialog.getByLabel(/^Task/).fill(task)
    await dialog.getByRole('button', { name: 'Launch', exact: true }).click()

    await expect(bobPage.getByRole('heading', { name: task, exact: true })).toBeVisible()
    const terminal = bobPage.locator('.xterm-rows:not([data-aether-frozen-view] *)')
    await expect(terminal).toContainText('agent-ready', { timeout: 3 * 60 * 1000 })
    await expect(terminal).toContainText(`login-seen:${ownerLogin}`)

    await bobPage.getByText('Task and details', { exact: true }).click()
    const metadata = bobPage.getByRole('group').filter({
      has: bobPage.getByText('Task and details', { exact: true }),
    })
    const row = (term: string) =>
      metadata
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
