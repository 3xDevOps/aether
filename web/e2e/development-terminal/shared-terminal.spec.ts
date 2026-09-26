import { readFileSync } from 'node:fs'
import path from 'node:path'
import type { Locator, Page } from '@playwright/test'
import type { DevControlStatusResult, DevTerminalListResult } from '../../src/lib/types'
import { expect, test } from '../fixtures'
import { dockerReachable } from '../harness/server'
import { memberID, seedWorkspace } from '../harness/setup'

const tui = readFileSync(new URL('./querying-tui.sh', import.meta.url), 'utf8')
const launch = `printf '%s' '{"name":"agent-tui","command":["dev-tui"],"cols":73,"rows":19}' | aether-internal terminal start --params-file -
while :; do sleep 1; done`

async function openDock(page: Page, url: string): Promise<Locator> {
  await page.goto(url)
  const sidebar = page.getByRole('button', { name: 'Expand sidebar' })
  if (await sidebar.isVisible()) {
    await sidebar.click()
    await page.getByRole('dialog', { name: 'Runs' }).getByRole('button', { name: /development terminal acceptance/ }).click()
  } else {
    await page.getByRole('complementary', { name: 'Runs' }).getByRole('button', { name: /development terminal acceptance/ }).click()
  }
  const dock = page.getByRole('region', { name: 'Terminal dock' })
  await dock.getByRole('tab', { name: /agent-tui/ }).click()
  await expect(dock.locator('.xterm-rows')).toContainText('Shared TUI: 界 λ é')
  return dock
}

test.skip(!dockerReachable(), 'requires a real Docker daemon and run container')

test('agent-created TUI shares authority, geometry, protocol responses and process lifetime', async ({ page, browser, aether }, testInfo) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('development-terminal')
  await seedWorkspace(alice, aether.server.addr, repo)
  const member = await memberID(alice)
  aether.installAgent(member, 'dev-tui', tui)
  aether.installAgent(member, 'claude', launch)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id, harness: 'claude', task: 'development terminal acceptance',
  })
  const list = () => alice.api.rpc<DevTerminalListResult>('dev.terminal.list', { run_id: run.id })
  await expect.poll(async () => (await list()).terminals.some((item) => item.name === 'agent-tui')).toBe(true)
  const terminal = (await list()).terminals.find((item) => item.name === 'agent-tui')!
  const target = { run_id: run.id, terminal_id: terminal.terminal_id, incarnation: terminal.incarnation }
  const surface = { kind: 'terminal', id: terminal.terminal_id, incarnation: terminal.incarnation }
  const status = () => alice.api.rpc<DevControlStatusResult>('dev.control.status', { run_id: run.id, surface })
  const home = aether.server.memberHome(member)
  const file = (name: string) => {
    try { return readFileSync(path.join(home, `development-tui-${name}`), 'utf8') } catch (error) {
      if (error instanceof Error && 'code' in error && error.code === 'ENOENT') return ''
      throw error
    }
  }
  await page.setViewportSize({ width: 1568, height: 1000 })
  const dock = await openDock(page, alice.url)
  expect((await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)).toMatchObject({ cols: 73, rows: 19 })
  await expect(dock.getByRole('button', { name: 'Stop terminal' })).toBeDisabled()
  await dock.getByRole('button', { name: 'Take shell control' }).click()
  if (await page.getByRole('button', { name: 'Confirm takeover' }).isVisible()) await page.getByRole('button', { name: 'Confirm takeover' }).click()
  await expect(dock.getByRole('button', { name: 'Release shell control' })).toBeVisible()
  const firstController = (await status()).controller!
  expect(firstController.kind).toBe('member')
  const input = dock.locator('.xterm-helper-textarea')
  await input.focus()
  await page.keyboard.type('XQ')
  await expect.poll(() => file('effects')).toBe('effect\n')
  await expect.poll(() => (file('input').match(/\x1b\[[?\d;]*c/g) ?? []).length).toBe(1)
  await expect.poll(() => (file('input').match(/\x1b\[[\d;]*R/g) ?? []).length).toBe(1)
  await expect.poll(() => file('input').includes('rgb:')).toBe(true)
  await dock.locator('.xterm-screen').click({ position: { x: 40, y: 12 } })
  await expect.poll(() => /\x1b\[<0;\d+;\d+M/.test(file('input'))).toBe(true)
  await input.focus()
  await page.keyboard.insertText('λ界')
  await expect.poll(() => file('input').includes('λ界')).toBe(true)
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.evaluate(() => navigator.clipboard.writeText('paste-界'))
  await page.keyboard.press('Control+Shift+V')
  await expect.poll(() => file('input').includes('paste-界')).toBe(true)
  await testInfo.attach('shared-alternate-screen', { body: await dock.screenshot(), contentType: 'image/png' })
  await expect(dock.getByRole('status').filter({ hasText: 'Controller: You' })).toBeVisible()

  const watcherContext = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const watcherPage = await watcherContext.newPage()
  try {
    const beforeWatch = (await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)!
    const watcher = await openDock(watcherPage, alice.url)
    await watcherPage.setViewportSize({ width: 360, height: 740 })
    expect((await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)).toMatchObject({ cols: beforeWatch.cols, rows: beforeWatch.rows })
    await expect(watcher.getByRole('button', { name: 'Stop terminal' })).toBeDisabled()
    await expect(watcher.getByRole('status').filter({ hasText: `Controller: ${member}` })).toBeVisible()
    await watcher.getByRole('button', { name: 'Take shell control' }).click()
    await watcherPage.getByRole('button', { name: 'Confirm takeover' }).click()
    await expect(watcher.getByRole('button', { name: 'Release shell control' })).toBeVisible()
    await expect(dock.getByRole('button', { name: 'Stop terminal' })).toBeDisabled()
    await expect(alice.api.rpc('dev.terminal.input', { ...target, control_session_id: firstController.control_session_id, control_generation: firstController.control_generation, kind: 'text', text: 'X' })).rejects.toThrow()
    expect(file('effects')).toBe('effect\n')
    await watcher.locator('.xterm-helper-textarea').focus()
    await watcherPage.keyboard.type('X')
    await expect.poll(() => file('effects')).toBe('effect\neffect\n')
    await watcher.getByRole('button', { name: 'Release shell control' }).click()
    await expect(watcher.getByRole('button', { name: 'Take shell control' })).toBeVisible()
    await watcher.getByRole('button', { name: 'Hide terminal' }).click()
    const heartbeat = file('heartbeat').length
    await expect.poll(() => file('heartbeat').length).toBeGreaterThan(heartbeat)
    await watcher.getByRole('button', { name: /Show agent-tui/ }).click()
    await expect(watcher.locator('.xterm-rows')).toContainText('Side effect written')
    expect(file('starts')).toBe('start\n')
    expect((await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)?.incarnation).toBe(terminal.incarnation)
  } finally { await watcherContext.close() }

  for (let index = 0; index < 3; index++) {
    await alice.api.rpc('dev.terminal.start', { run_id: run.id, name: `capacity-${index}`, command: ['sleep', '600'] })
  }
  await expect(alice.api.rpc('dev.terminal.start', { run_id: run.id, name: 'over-capacity' })).rejects.toThrow()
  await expect(dock.getByRole('button', { name: 'Add terminal tab' })).toBeHidden()

  await expect(dock.getByRole('status').filter({ hasText: 'Controller: Nobody' })).toBeVisible()
  await dock.getByRole('button', { name: 'Take shell control' }).click()
  if (await page.getByRole('button', { name: 'Confirm takeover' }).isVisible()) await page.getByRole('button', { name: 'Confirm takeover' }).click()
  await expect(dock.getByRole('button', { name: 'Release shell control' })).toBeVisible()
  await dock.getByRole('button', { name: 'Screenshot', exact: true }).click()
  await expect(dock.getByRole('status').filter({ hasText: /Captured/ })).toBeVisible()
  await dock.getByRole('button', { name: 'Stop terminal' }).click()
  await page.getByRole('button', { name: 'Confirm stop' }).click()
  await expect.poll(async () => (await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)?.process.state).not.toBe('running')
  await page.reload()
  await page.getByRole('region', { name: 'Terminal dock' }).getByRole('tab', { name: /agent-tui/ }).click()
  await expect(page.getByRole('region', { name: 'Terminal dock' }).getByRole('button', { name: 'Stop terminal' })).toBeDisabled()
  expect(file('starts')).toBe('start\n')
  expect((file('input').match(/\x1b\[[?\d;]*c/g) ?? []).length).toBe(1)
})
