// The assistant's sign-in in Settings and in the assistant panel. The
// agent's real sign-in needs a provider account CI does not have, so the
// sign-in socket here plays the server's side: the attach ack, the output of
// the pinned claude-agent-acp's sign-in, and a clean close when the sign-in
// exits. The page is the real dashboard on a real gateway and server.

import type { Page, WebSocketRoute } from '@playwright/test'
import { expect, test } from './fixtures'

const prompt = 'Paste code here if prompted >'
const url = 'https://claude.com/cai/oauth/authorize?code=true&client_id=client&response_type=code&state=state'
const output = `If the browser didn't open, visit: \x1b]8;;${url}\x07\x1b[94m${url}\x1b[39m\x1b]8;;\x07\r\n${prompt} `

interface SignIn {
  socket: WebSocketRoute
  resizes: { cols: number; rows: number }[]
  input: string
  closed: boolean
}

async function playSignIn(page: Page, state: string, recorded = true) {
  const signIns: SignIn[] = []
  await page.route('**/api/v1/assistant.status', (route) => route.fulfill({
    json: {
      state,
      sign_in_recorded: recorded || signIns.length > 0,
      agent: 'claude',
      agents: [{ name: 'claude', display_name: 'Claude Code', ready: false, reason: 'Not signed in.' }],
      confirmations: [],
      auto_approve: false,
      suggest_auto_approve: false,
      share_view: true,
    },
  }))
  await page.routeWebSocket(/\/ws\/terminal\?assistant_login=claude/, (socket) => {
    const signIn: SignIn = { socket, resizes: [], input: '', closed: false }
    signIns.push(signIn)
    socket.onClose(() => {
      signIn.closed = true
    })
    socket.onMessage((message) => {
      if (typeof message !== 'string') return
      const frame = JSON.parse(message) as { type?: string; cols: number; rows: number; data?: string }
      if (frame.type === 'resize') signIn.resizes.push({ cols: frame.cols, rows: frame.rows })
      else if (frame.type === 'input') signIn.input += frame.data
      else if (frame.type === undefined) {
        socket.send(JSON.stringify({ ok: true, has_control: true, cols: frame.cols, rows: frame.rows, replay: 0 }))
        socket.send(Buffer.from(output))
      }
    })
  })
  return signIns
}

async function expectSettled(page: Page, signIn: SignIn) {
  const pane = page.getByRole('region', { name: 'Sign in to Claude Code' })
  await expect(pane.locator('.xterm-rows')).toContainText(prompt)
  const host = pane.locator('.xterm').locator('..')
  const measure = () => host.evaluate((element) => ({
    height: Math.round(element.getBoundingClientRect().height),
    rows: element.querySelectorAll('.xterm-rows > div').length,
  }))
  await page.waitForTimeout(300)
  const settled = await measure()
  const resizes = signIn.resizes.length
  await page.waitForTimeout(1_000)
  expect(await measure()).toEqual(settled)
  expect(signIn.resizes.length).toBe(resizes)
  expect(settled.height).toBeLessThanOrEqual(page.viewportSize()!.height)
  return settled
}

test('the sign-in terminal in Settings keeps its size through open, input, resize, reopen and exit', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const signIns = await playSignIn(page, 'idle')
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto(alice.url)
  await page.getByRole('navigation', { name: 'Aether' }).getByRole('button', { name: 'Settings', exact: true }).click()
  await page.getByRole('navigation', { name: 'Settings' }).getByRole('button', { name: 'Assistant', exact: true }).click()
  const section = page.locator('#assistant-sign-in')

  await section.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect.poll(() => signIns.length).toBe(1)
  const desktop = await expectSettled(page, signIns[0]!)

  const pane = page.getByRole('region', { name: 'Sign in to Claude Code' })
  await pane.locator('.xterm').click()
  await page.keyboard.type('code-123')
  await expect.poll(() => signIns[0]!.input).toBe('code-123')

  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(() => signIns[0]!.resizes.at(-1)?.cols ?? Infinity).toBeLessThan(80)
  const narrow = await expectSettled(page, signIns[0]!)
  expect(narrow.rows).toBeGreaterThan(0)
  await page.setViewportSize({ width: 1280, height: 800 })
  expect((await expectSettled(page, signIns[0]!)).rows).toBe(desktop.rows)

  await pane.getByRole('button', { name: 'Close' }).click()
  await expect(pane).toBeHidden()
  await expect.poll(() => signIns[0]!.closed).toBe(true)

  await section.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect.poll(() => signIns.length).toBe(2)
  expect((await expectSettled(page, signIns[1]!)).rows).toBe(desktop.rows)
  await signIns[1]!.socket.close({ code: 1000 })
  await pane.getByRole('button', { name: 'Done' }).click()
  await expect(pane).toBeHidden()
  expect(signIns).toHaveLength(2)
})

test('the sign-in terminal in the assistant panel keeps its size', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const signIns = await playSignIn(page, 'setup')
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto(alice.url)
  await page.getByRole('button', { name: 'Assistant' }).first().click()
  await page.getByRole('list', { name: 'Agents' }).getByRole('button', { name: 'Sign in' }).click()
  await expect.poll(() => signIns.length).toBe(1)
  await page.getByRole('region', { name: 'Sign in to Claude Code' }).getByRole('button', { name: 'Show terminal' }).click()
  await expectSettled(page, signIns[0]!)
})

test('the assistant panel starts the first sign-in on its own, once', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await alice.api.local('link.apply', { addr: aether.server.addr, name: alice.name })
  const signIns = await playSignIn(page, 'setup', false)
  await page.goto(alice.url)
  await page.getByRole('button', { name: 'Assistant' }).first().click()
  const pane = page.getByRole('region', { name: 'Sign in to Claude Code' })
  await expect(pane.getByRole('link', { name: 'Open Claude sign-in' })).toHaveAttribute('href', url)
  await expect(pane.locator('.xterm')).toHaveCount(0)
  expect(signIns).toHaveLength(1)

  await pane.getByLabel('Paste the code').fill('code-123#state')
  await pane.getByRole('button', { name: 'Send code' }).click()
  await expect.poll(() => signIns[0]!.input).toBe('code-123#state\r')
  await signIns[0]!.socket.close({ code: 1000 })
  await expect(pane.locator('.xterm-rows')).toContainText(prompt)
  await pane.getByRole('button', { name: 'Done' }).click()
  await expect(page.getByRole('list', { name: 'Agents' })).toBeVisible()

  await page.getByRole('button', { name: 'Close assistant' }).click()
  await page.reload()
  await page.getByRole('button', { name: 'Assistant' }).first().click()
  await expect(page.getByRole('list', { name: 'Agents' })).toBeVisible()
  expect(signIns).toHaveLength(1)
})
