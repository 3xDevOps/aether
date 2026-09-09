// Opening a run while its container is still being provisioned. `run.launch`
// provisions synchronously (internal/scheduler/launch.go), so the run sits in
// queued and then provisioning for as long as the checkout and `docker create`
// take, while the run.status events have already put its card on the board.
// Opening it in that window is the first thing anyone does after launching.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { seedWorkspace } from './harness/setup'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')

test('a run opened while its container starts says so', async ({ page, aether }) => {
  // The store takes a new run's status from the event, not from the fetch it
  // makes alongside it (`applyRunStatus` after `runGet` in store/sync.ts), so
  // withholding the move to running is what holds the window this test is
  // about open. Otherwise its length is however long `docker create` takes.
  // Installed before the first navigation, the only point where the page's
  // WebSocket can be replaced.
  let holding = false
  let provisioning: string | undefined
  const held: (string | Buffer)[] = []
  const gateway: { toPage?: (message: string | Buffer) => void } = {}
  await page.routeWebSocket(/\/ws\/events/, (ws) => {
    const server = ws.connectToServer()
    gateway.toPage = (message) => ws.send(message)
    server.onMessage((message) => {
      const status = runStatus(message)
      // `run.launch` only answers with the run's id once the run is already
      // running, too late to name the frame that has to be held. The run
      // names itself instead: its first status frame is queued ->
      // provisioning, which the launch below is the only source of.
      if (holding && status?.to === 'provisioning') provisioning ??= status.runID
      // The hold starts at the move to running and covers every frame after
      // it, in order. Forwarding a later frame past a held one would carry
      // the client's cursor beyond it, and the replay would then arrive
      // below that cursor - which `applyEvent` in store/sync.ts reads as a
      // restarted event log and answers with a full resubscribe and
      // snapshot, the path this test does not mean to exercise.
      const becomesRunning =
        provisioning !== undefined &&
        status?.to === 'running' &&
        status.runID === provisioning
      if (holding && (held.length > 0 || becomesRunning)) {
        held.push(message)
        return
      }
      ws.send(message)
    })
    ws.onMessage((message) => server.send(message))
  })

  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  await page.goto(alice.url)
  const sidebar = page.getByRole('complementary')
  // The page must be subscribed before the launch, or the run's frames are
  // never sent to it and there is nothing to hold. Hydration runs behind the
  // subscription (store/sync.ts), so a settled empty run list proves it.
  await expect(sidebar.getByText('No runs yet.')).toBeVisible()

  holding = true
  // Not awaited: the call only returns once the run is running, and the
  // window this test is about is the one before that. Its rejection is
  // handled now so a launch failure surfaces at the await below as itself,
  // rather than as an unhandled rejection under whichever assertion is
  // running at the time.
  const launched = alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'fake',
    task: 'the provisioning run',
  })
  launched.catch(() => {})

  await sidebar.getByRole('button', { name: /the provisioning run/ }).click()
  await expect(
    page.getByRole('heading', { name: 'the provisioning run', exact: true }),
  ).toBeVisible()

  // The container is still being built, so the tab says that rather than
  // attaching to a session that cannot exist yet and painting the gateway's
  // refusal as a dead terminal.
  await expect(page.getByText("Starting the run's container")).toBeVisible()
  // And it is the only thing said: a connection state, the gateway's "no
  // session for run" refusal with its Retry, and "This run is not running"
  // all render in the same row, and each would be a second sentence pulling
  // against the spinner.
  await expect(page.getByText('Offline')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Retry' })).toHaveCount(0)
  await expect(page.getByText('This run is not running')).toHaveCount(0)

  // And the wait ends by itself: the run turning running attaches the tab,
  // with nothing pressed.
  await launched
  holding = false
  for (const message of held.splice(0)) gateway.toPage?.(message)

  await expect(page.getByText('Attached')).toBeVisible()
  await expect(page.getByText("Starting the run's container")).toBeHidden()
  await expect(page.locator('.xterm-rows')).toContainText('agent-ready', {
    timeout: 3 * 60 * 1000,
  })
})

/** The run and target status of a gateway frame, for run.status frames. */
function runStatus(message: string | Buffer): { runID?: string; to?: string } | null {
  try {
    const event = JSON.parse(message.toString()) as {
      type?: string
      run_id?: string
      payload?: { to?: string }
    }
    if (event.type !== 'run.status') return null
    return { runID: event.run_id, to: event.payload?.to }
  } catch {
    return null
  }
}
