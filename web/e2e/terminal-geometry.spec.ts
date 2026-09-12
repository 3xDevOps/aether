import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

// Cursor-addressed output exposes a wrong grid where plain echo does not.
const painter = `stty -echo
paint() {
  set -- $(stty size)
  printf '\\033[2J\\033[H'
  printf '\\033[%s;%sHX' "$1" "$2"
}
trap paint WINCH
paint
while :; do
  if IFS= read -r line; then paint; fi
done
`

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('new runs keep desktop viewers on the shared grid through resize and reattach', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', painter)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'shared geometry regression',
  })
  const url = new URL(alice.url)
  const writer = new WebSocket(`ws://${url.host}/ws/attach/${run.id}?token=${url.searchParams.get('token')}`)
  try {
    await new Promise<void>((resolve, reject) => {
      writer.addEventListener('error', () => reject(new Error('writer socket failed')))
      writer.addEventListener('open', () => writer.send(JSON.stringify({ write: true, cols: 60, rows: 18 })))
      writer.addEventListener('message', (event) => {
        if (typeof event.data !== 'string') return
        const ack = JSON.parse(event.data)
        if (!ack.ok) reject(new Error(ack.error ?? 'writer refused'))
        else resolve()
      }, { once: true })
    })
    await page.setViewportSize({ width: 1568, height: 1000 })
    await page.goto(alice.url)
    await page.getByRole('complementary', { name: 'Runs' }).getByRole('button', { name: /shared geometry regression/ }).click()
    const rows = page.locator('.xterm-rows > div')
    const assertGrid = async (cols: number, height: number) => {
      await expect(rows).toHaveCount(height)
      writer.send(new TextEncoder().encode('\r'))
      await expect(rows.nth(height - 1)).toHaveText(`${' '.repeat(cols - 1)}X`)
    }
    await assertGrid(60, 18)
    writer.send(JSON.stringify({ type: 'resize', cols: 72, rows: 22 }))
    await assertGrid(72, 22)
    // The larger viewer must keep proposing its pane, not feed 60x18 back
    // into the minimum and stop the shared terminal ever growing again.
    await page.getByRole('button', { name: 'Increase terminal text size' }).click()
    await assertGrid(72, 22)
    await page.getByRole('button', { name: 'Steering', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Take control' })).toBeVisible()
    await assertGrid(72, 22)
    await page.getByRole('tab', { name: 'Overview', exact: true }).click()
    await page.getByRole('tab', { name: 'Terminal', exact: true }).click()
    await assertGrid(72, 22)
  } finally {
    writer.close()
  }
})
