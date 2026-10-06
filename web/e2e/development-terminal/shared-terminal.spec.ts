import { readFileSync } from 'node:fs'
import path from 'node:path'
import type { Locator, Page } from '@playwright/test'
import type { DevControlStatusResult, DevTerminalListResult, DevTerminalScreenshotResult } from '../../src/lib/types'
import { expect, test } from '../fixtures'
import { dockerReachable } from '../harness/server'
import { memberID, seedWorkspace } from '../harness/setup'

const tui = readFileSync(new URL('./querying-tui.sh', import.meta.url), 'utf8')
// The executable starts while the container is still provisioning. Discover
// live authority before issuing the one terminal-start mutation, just as an
// agent must; never retry a mutation whose outcome could be ambiguous.
const launch = `remaining=100
while :; do
  status=$(aether-internal status) || exit 1
  case "$status" in *'"dev.terminal.start"'*) break ;; esac
  remaining=$((remaining - 1))
  if [ "$remaining" -eq 0 ]; then printf '%s\\n' "$status" >&2; exit 1; fi
  sleep 0.1
done
printf '%s' '{"name":"agent-tui","command":["dev-tui"],"cols":73,"rows":19}' | aether-internal terminal start --params-file - || exit 1
while :; do sleep 1; done`

async function openDock(page: Page, url: string): Promise<Locator> {
  await page.goto(url)
  const sidebar = page.getByRole('button', { name: /^Open sidebar/ })
  await expect(sidebar.or(page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' }))).toBeVisible()
  if (await sidebar.isVisible()) {
    await sidebar.click()
    await page.getByRole('dialog', { name: 'Aether' }).getByRole('button', { name: /development terminal acceptance/ }).click()
  } else {
    await page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' }).getByRole('button', { name: /development terminal acceptance/ }).click()
  }
  const dock = page.getByRole('region', { name: 'Terminal dock' })
  await dock.getByRole('tab', { name: /agent-tui/ }).click()
  await expect(dock.locator('.xterm-rows')).toContainText('Shared TUI: 界 λ é')
  return dock
}

async function terminalActions(page: Page, dock: Locator): Promise<Locator> {
  await dock.getByRole('button', { name: 'More terminal actions' }).click()
  return page.getByRole('menu')
}

async function expectCannotStop(page: Page, dock: Locator): Promise<void> {
  const menu = await terminalActions(page, dock)
  await expect(menu.getByRole('menuitem', { name: 'Stop terminal' })).toBeDisabled()
  await menu.press('Escape')
  await expect(menu).toBeHidden()
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
  await expectCannotStop(page, dock)
  const initialController = (await status()).controller
  await dock.getByRole('button', { name: 'Take shell control' }).click()
  if (initialController) {
    await page.getByRole('dialog', { name: 'Take shell control?' })
      .getByRole('button', { name: 'Confirm takeover' }).click()
  }
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
    await expectCannotStop(watcherPage, watcher)
    await expect(watcher.getByRole('status').filter({ hasText: `Controller: ${member}` })).toBeVisible()
    await watcher.getByRole('button', { name: 'Take shell control' }).click()
    await watcherPage.getByRole('dialog', { name: 'Take shell control?' })
      .getByRole('button', { name: 'Confirm takeover' }).click()
    await expect(watcher.getByRole('button', { name: 'Release shell control' })).toBeVisible()
    await expectCannotStop(page, dock)
    await expect(alice.api.rpc('dev.terminal.input', { ...target, control_session_id: firstController.control_session_id, control_generation: firstController.control_generation, kind: 'text', text: 'X' })).rejects.toThrow()
    expect(file('effects')).toBe('effect\n')
    await watcher.locator('.xterm-helper-textarea').focus()
    await watcherPage.keyboard.type('X')
    await expect.poll(() => file('effects')).toBe('effect\neffect\n')
    await watcher.getByRole('button', { name: 'Release shell control' }).click()
    await expect(watcher.getByRole('button', { name: 'Take shell control' })).toBeVisible()
    await (await terminalActions(watcherPage, watcher))
      .getByRole('menuitem', { name: 'Hide terminal' }).click()
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
  await expect(dock.getByRole('button', { name: 'Release shell control' })).toBeVisible()
  // A first capture includes companion launch and has a bounded 90s API
  // lifecycle; do not abort that request at the ordinary 30s locator limit.
  const captureResponse = page.waitForResponse((response) => response.url().endsWith('/api/v1/dev.terminal.screenshot'), { timeout: 95_000 })
  await (await terminalActions(page, dock))
    .getByRole('menuitem', { name: 'Screenshot', exact: true }).click()
  const captured = await captureResponse
  expect(captured.ok(), await captured.text()).toBe(true)
  const { artifact } = await captured.json() as DevTerminalScreenshotResult
  expect(artifact).toMatchObject({ source: 'terminal', run_id: run.id, terminal_id: terminal.terminal_id, incarnation: terminal.incarnation })
  const image = await fetch(new URL(`/api/v1/dev/${run.id}/artifacts/${artifact.id}`, alice.url), {
    headers: { authorization: `Bearer ${new URL(alice.url).searchParams.get('token')}` },
    signal: AbortSignal.timeout(30_000),
  })
  expect(image.status).toBe(200)
  expect(image.headers.get('content-type')).toBe('image/png')
  await testInfo.attach('shared-terminal-capture', { body: Buffer.from(await image.arrayBuffer()), contentType: 'image/png' })
  await testInfo.attach('shared-terminal-capture-metadata', { body: JSON.stringify(artifact), contentType: 'application/json' })
  await expect(dock.getByRole('status').filter({ hasText: /Captured/ })).toBeVisible()
  await (await terminalActions(page, dock))
    .getByRole('menuitem', { name: 'Stop terminal' }).click()
  await page.getByRole('dialog', { name: 'Stop this terminal process?' })
    .getByRole('button', { name: 'Confirm stop' }).click()
  await expect.poll(async () => (await list()).terminals.find((item) => item.terminal_id === terminal.terminal_id)?.process.state).not.toBe('running')
  await page.reload()
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('region', { name: 'Runs' }).getByRole('button', { name: /development terminal acceptance/ }).click()
  await page.getByRole('region', { name: 'Terminal dock' }).getByRole('tab', { name: /agent-tui/ }).click()
  await expectCannotStop(page, page.getByRole('region', { name: 'Terminal dock' }))
  expect(file('starts')).toBe('start\n')
  expect((file('input').match(/\x1b\[[?\d;]*c/g) ?? []).length).toBe(1)
})
