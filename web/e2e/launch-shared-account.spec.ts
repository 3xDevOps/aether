// Launching on a teammate's account: Alice shares hers with Bob, Carol does
// not. Bob's launch dialog lists his own account and Alice's, each with
// whether Claude Code can launch there, and never Carol's; Launch on
// Alice's sends her account to run.launch. The gateway the dashboard calls
// refuses a launch on Carol's account, and on Alice's once she revokes,
// with the server's reason.
//
// With a Docker daemon the run starts and records Alice's account; without
// one the launch fails in the scheduler, after the server admitted the
// account.

import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

test('a member picks an account shared with them to launch on, and the server refuses any other', async ({ browser, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('shop')
  await seedWorkspace(alice, aether.server.addr, repo)
  const bob = await aether.member('bob')
  await bob.api.local('link.apply', { addr: aether.server.addr, invite: await aether.invite(alice), name: 'bob' })
  const carol = await aether.member('carol')
  await carol.api.local('link.apply', { addr: aether.server.addr, invite: await aether.invite(alice), name: 'carol' })
  const [aliceID, bobID, carolID] = await Promise.all([memberID(alice), memberID(bob), memberID(carol)])
  for (const id of [aliceID, bobID, carolID]) aether.installAgent(id, 'claude')
  const login = path.join(aether.server.memberHome(aliceID), '.claude', '.credentials.json')
  mkdirSync(path.dirname(login), { recursive: true })
  writeFileSync(login, '{}\n', { mode: 0o600 })
  const name = async (member: typeof alice) => (await member.api.rpc<{ member: { display_name: string } }>('server.info')).member.display_name
  const [aliceName, bobName, carolName] = await Promise.all([name(alice), name(bob), name(carol)])
  await alice.api.rpc('account.share', { member_id: bobID })

  const page = await (await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' })).newPage()
  await page.goto(bob.url)
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'New session' }).click()
  const dialog = page.getByRole('dialog', { name: 'New session' })
  await expect(dialog.getByRole('radio', { name: /^Claude Code/ })).toBeChecked()
  const accounts = dialog.getByRole('radiogroup', { name: 'Account' })
  await expect(accounts.getByRole('radio')).toHaveText([
    new RegExp(`^${bobName}YouSign in after launch$`),
    new RegExp(`^${aliceName}SharedReady$`),
  ])
  await expect(accounts.getByRole('radio', { name: new RegExp(`^${carolName}`) })).toHaveCount(0)
  await accounts.getByRole('radio', { name: new RegExp(`^${aliceName}`) }).click()
  await expect(dialog.getByText(`Uses ${aliceName}'s Claude Code login and quota.`, { exact: false })).toBeVisible()
  await expect(dialog.getByRole('radio', { name: /^Claude Code/ })).toBeChecked()
  const sent = page.waitForRequest((request) => request.url().endsWith('/api/v1/run.launch'))
  await dialog.getByRole('button', { name: 'Launch', exact: true }).click()
  expect((await sent).postDataJSON()).toMatchObject({ harness: 'claude', account_member_id: aliceID })
  if (dockerReachable()) {
    await expect.poll(async () => (await bob.api.rpc<{ runs: { account_member_id: string }[] }>('run.list')).runs.map((run) => run.account_member_id)).toEqual([aliceID])
  } else {
    const failed = dialog.getByRole('alert').filter({ hasText: 'Launch failed' })
    await expect(failed).toContainText('scheduler:')
    await expect(failed).not.toContainText('has not shared')
  }

  const { workspaces } = await bob.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const launch = (account: string) => bob.api.rpc('run.launch', { workspace_id: workspaces[0]!.id, harness: 'claude', mode: 'tui', account_member_id: account })
  await expect(launch(carolID)).rejects.toThrow(`member ${carolID} has not shared their account with you`)
  await alice.api.rpc('account.revoke', { member_id: bobID })
  await expect(launch(aliceID)).rejects.toThrow(`member ${aliceID} has not shared their account with you`)
})
