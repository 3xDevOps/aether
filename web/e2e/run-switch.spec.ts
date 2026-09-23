// Opening one run after another on the Terminal tab. The run-detail view is
// reused across the switch, so the pane has to be cleared by the switch
// itself: a run whose attach is never answered - a finished run with no
// recorded terminal - would otherwise keep showing the run before it.

import { expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'
import { OnboardingWizard } from './pages/wizard'

/** What the installed `claude` shim runs: the seed repository's own script,
 * from the run checkout the scheduler mounts at /workspace. */
const agentShim = 'sh /workspace/agent.sh'

test.skip(!dockerReachable(), 'a run needs a reachable Docker daemon')
type BusyRunWriter = {
  socket: WebSocket
  generation: () => number
  startGap: () => Promise<void>
  release: () => Promise<void>
}

async function primeBusyRun(
  url: URL,
  runID: string,
  label: string,
): Promise<BusyRunWriter> {
  const socket = new WebSocket(
    `ws://${url.host}/ws/attach/${runID}?token=${url.searchParams.get('token')}`,
  )
  socket.binaryType = 'arraybuffer'
  let generation = 0
  let acknowledged = false
  let output = ''
  let rejected: Error | undefined
  let rejectAcknowledged: ((error: Error) => void) | undefined
  const outputWaiters = new Map<string, { resolve: () => void; reject: (error: Error) => void }>()
  const controlWaiters = new Map<number, { resolve: () => void; reject: (error: Error) => void }>()
  const rejectWaiters = (error: Error) => {
    rejected = error
    rejectAcknowledged?.(error)
    rejectAcknowledged = undefined
    for (const waiter of outputWaiters.values()) waiter.reject(error)
    for (const waiter of controlWaiters.values()) waiter.reject(error)
    outputWaiters.clear()
    controlWaiters.clear()
  }
  const waitForOutput = (marker: string): Promise<void> => {
    if (rejected) return Promise.reject(rejected)
    if (output.includes(marker)) return Promise.resolve()
    return new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        outputWaiters.delete(marker)
        reject(new Error(`${label} did not produce ${marker}`))
      }, 180_000)
      outputWaiters.set(marker, {
        resolve: () => {
          clearTimeout(timeout)
          outputWaiters.delete(marker)
          resolve()
        },
        reject: (error) => {
          clearTimeout(timeout)
          outputWaiters.delete(marker)
          reject(error)
        },
      })
    })
  }
  const waitForControl = (requestID: number): Promise<void> => {
    if (rejected) return Promise.reject(rejected)
    return new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        controlWaiters.delete(requestID)
        reject(new Error(`${label} control request ${requestID} timed out`))
      }, 30_000)
      controlWaiters.set(requestID, {
        resolve: () => {
          clearTimeout(timeout)
          controlWaiters.delete(requestID)
          resolve()
        },
        reject: (error) => {
          clearTimeout(timeout)
          controlWaiters.delete(requestID)
          reject(error)
        },
      })
    })
  }
  const acknowledgedPromise = new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`${label} writer ack timed out`)), 180_000)
    rejectAcknowledged = reject
    const acknowledge = () => {
      clearTimeout(timeout)
      rejectAcknowledged = undefined
      acknowledged = true
      resolve()
    }
    socket.addEventListener('message', (event) => {
      if (typeof event.data === 'string') {
        let frame: {
          ok?: boolean
          error?: string
          control_generation?: number
          type?: string
          request_id?: number
        }
        try {
          frame = JSON.parse(event.data) as typeof frame
        } catch {
          return
        }
        if (frame.type === 'control' && frame.request_id !== undefined) {
          const waiter = controlWaiters.get(frame.request_id)
          if (!waiter) return
          if (frame.ok) waiter.resolve()
          else waiter.reject(new Error(frame.error ?? `${label} control request refused`))
          return
        }
        if (frame.ok === undefined || acknowledged) return
        if (!frame.ok) {
          reject(new Error(frame.error ?? `${label} writer refused`))
          return
        }
        generation = frame.control_generation ?? 0
        acknowledge()
        socket.send(JSON.stringify({ type: 'input', data: `${label}\r`, control_generation: generation }))
      } else {
        output = (output + new TextDecoder().decode(new Uint8Array(event.data as ArrayBuffer))).slice(-8192)
        for (const [marker, waiter] of outputWaiters) {
          if (output.includes(marker)) waiter.resolve()
        }
      }
    })
  })
  socket.addEventListener('error', () => rejectWaiters(new Error(`${label} writer socket failed`)))
  socket.addEventListener('close', () => rejectWaiters(new Error(`${label} writer socket closed`)))
  socket.addEventListener('open', () => {
    socket.send(
      JSON.stringify({
        write: true,
        screen: false,
        interactive: true,
        cols: 120,
        rows: 40,
        control_session_id: `switch-${label}`,
      }),
    )
  })
  await acknowledgedPromise
  await waitForOutput(`${label}-CURRENT`)
  return {
    socket,
    generation: () => generation,
    startGap: async () => {
      const gap = waitForOutput(`${label}-GAP`)
      socket.send(JSON.stringify({ type: 'input', data: 'start-gap\r', control_generation: generation }))
      await gap
    },
    release: async () => {
      if (!acknowledged || socket.readyState !== WebSocket.OPEN) return
      const requestID = 1
      const control = waitForControl(requestID)
      socket.send(
        JSON.stringify({
          type: 'control',
          request_id: requestID,
          write: false,
          control_generation: generation,
        }),
      )
      await control
    },
  }
}

test('a second run never shows the first run output', async ({ page, aether }) => {
  // Installed before the first navigation, the only point where the page's
  // WebSocket can be replaced. Every attach reaches the server except the one
  // named here, which is left unanswered the way the server's refusal of a
  // transcript-less run leaves the pane.
  let unanswered = ''
  await page.routeWebSocket(/\/ws\/attach\//, (ws) => {
    const runID = new URL(ws.url()).pathname.split('/').pop() ?? ''
    if (runID !== unanswered) ws.connectToServer()
  })

  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.push().click()
  await expect(wizard.repository.section).toContainText('Pushed main to aether')
  await wizard.repository.continue().click()
  // The picker offers only the agents this account has installed.
  aether.installAgent(await memberID(alice), 'claude', agentShim)
  await wizard.agents.skip().click()
  await wizard.expectStep('First run')
  await wizard.firstRun.launch('claude', 'write the result file')

  const pane = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible')
  await expect(pane).toContainText('agent-ready', { timeout: 3 * 60 * 1000 })

  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
    workspace_id: workspaces[0].id,
    harness: 'claude',
    task: 'the second run',
  })
  unanswered = run.id

  const sidebar = page.getByRole('complementary')
  await sidebar.getByRole('button', { name: /the second run/ }).click()
  await expect(
    page.getByRole('heading', { name: 'the second run', exact: true }),
  ).toBeVisible()
  // The attach is still unanswered, so nothing has written to this pane yet.
  await expect(page.getByText('Connecting')).toBeVisible()
  await expect(pane).not.toContainText('agent-ready')
})

test('busy screens show the current output again after leaving the board', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  const aliceID = await memberID(alice)
  const runs: Array<{ task: string; label: string; screen: string; gap: string }> = [
    { task: 'busy terminal A', label: 'BUSY-A', screen: 'BUSY-A-CURRENT', gap: 'BUSY-A-GAP' },
    { task: 'busy terminal B', label: 'BUSY-B', screen: 'BUSY-B-CURRENT', gap: 'BUSY-B-GAP' },
    { task: 'busy terminal C', label: 'BUSY-C', screen: 'BUSY-C-CURRENT', gap: 'BUSY-C-GAP' },
    { task: 'busy terminal D', label: 'BUSY-D', screen: 'BUSY-D-CURRENT', gap: 'BUSY-D-GAP' },
  ]
  const busyFill = 'x'.repeat(120)
  // The shim is installed once. Each run is assigned its label over the real
  // PTY before the next run is launched, so concurrent startup cannot replace
  // the executable a previous run is about to invoke.
  aether.installAgent(
    aliceID,
    'claude',
    `stty -echo
IFS= read -r label
i=0
while [ "$i" -lt 24000 ]; do
  printf '\\033[2J\\033[H%s-HIST-%05d-${busyFill}\\r\\n' "$label" "$i"
  i=$((i + 1))
done
printf '\\033[2J\\033[H%s-CURRENT\\r\\n' "$label"
while IFS= read -r command; do
  if [ "$command" = "start-gap" ]; then
    i=0
    while [ "$i" -lt 120 ]; do
      printf '\\033[H%s-CURRENT\\r\\n%s-GAP-%03d-A\\r\\n%s-GAP-%03d-B\\r\\n' "$label" "$label" "$i" "$label" "$i"
      i=$((i + 1))
      sleep 1
    done
  fi
done
`,
  )
  const launched: Array<{
    id: string
    task: string
    label: string
    screen: string
    gap: string
    writer: BusyRunWriter
  }> = []
  for (const item of runs) {
    const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
      'workspace.list',
    )
    const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
      workspace_id: workspaces[0].id,
      harness: 'claude',
      task: item.task,
    })
    const writer = await primeBusyRun(new URL(alice.url), run.id, item.label)
    launched.push({ id: run.id, ...item, writer })
  }

  const attachHeaders: Array<{
    runID: string
    resume?: boolean
    resume_id?: string
    cursor?: number
  }> = []
  await page.routeWebSocket(/\/ws\/attach\//, (socket) => {
    const runID = new URL(socket.url()).pathname.split('/').pop() ?? ''
    const server = socket.connectToServer()
    socket.onMessage((message) => {
      if (typeof message === 'string') {
        try {
          const frame = JSON.parse(message) as {
            screen?: boolean
            interactive?: boolean
            resume?: boolean
            resume_id?: string
            cursor?: number
          }
          if (frame.screen === true || frame.interactive === true) {
            attachHeaders.push({ runID, ...frame })
          }
        } catch {
          // Forward the real protocol frame unchanged.
        }
      }
      server.send(message)
    })
    server.onMessage((message) => socket.send(message))
  })

  await page.setViewportSize({ width: 1568, height: 1000 })
  await page.goto(alice.url)
  const sidebar = page.getByRole('complementary', { name: 'Runs' })
  const surfaces = page.getByRole('navigation', { name: 'Surfaces' })
  const open = (task: string) => sidebar.getByRole('button', { name: task }).click()
  const board = () => surfaces.getByRole('button', { name: 'Board', exact: true }).click()
  await open(launched[0].task)
  const rows = page.locator('.xterm-rows:not([data-aether-frozen-view] *):visible')
  await expect(rows).toContainText(launched[0].screen, { timeout: 30_000 })
  await board()
  // Start A's stream only after the browser has seen the current screen.
  // Leaving the route closes that terminal; the gap still has to be on the
  // live screen when the run is opened again.
  await launched[0].writer.startGap()
  await open(launched[1].task)
  await expect(rows).toContainText(launched[1].screen, { timeout: 30_000 })
  await board()
  await launched[1].writer.startGap()
  await open(launched[2].task)
  await expect(rows).toContainText(launched[2].screen, { timeout: 30_000 })
  await board()
  await launched[2].writer.startGap()
  await open(launched[3].task)
  await expect(rows).toContainText(launched[3].screen, { timeout: 30_000 })
  await board()
  await launched[3].writer.startGap()
  await open(launched[0].task)
  await expect(rows).toContainText(launched[0].screen, { timeout: 30_000 })
  await expect(rows).toContainText(launched[0].gap, { timeout: 30_000 })
  await expect(page.getByRole('status', { name: 'Restoring terminal history' })).toBeHidden()
  const returned = attachHeaders.filter((header) => header.runID === launched[0].id)
  // Leaving the route closes the terminal. Coming back is a new compact
  // screen, not a resume of a parked buffer. The gap is on that live screen.
  expect(returned.length).toBeGreaterThanOrEqual(2)
  expect(returned.at(-1)?.resume).not.toBe(true)
  for (const item of launched) {
    await item.writer.release()
    item.writer.socket.close()
  }
})

test('an owner returning to a run keeps steering it', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  await seedWorkspace(alice, aether.server.addr, repo)
  aether.installAgent(await memberID(alice), 'claude', 'sleep 600')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>(
    'workspace.list',
  )
  const tasks = ['steered run A', 'steered run B']
  for (const task of tasks) {
    const { run } = await alice.api.rpc<{ run: { id: string } }>('run.launch', {
      workspace_id: workspaces[0].id,
      harness: 'claude',
      task,
    })
    // Provisioning must not happen inside the timed switch below, or A's
    // lease could expire and the return would pass as a fresh acquisition.
    await expect
      .poll(
        async () =>
          (await alice.api.rpc<{ run: { status: string } }>('run.get', { run_id: run.id })).run
            .status,
        { timeout: 3 * 60 * 1000 },
      )
      .toBe('running')
  }

  await page.goto(alice.url)
  const sidebar = page.getByRole('complementary', { name: 'Runs' })
  const steering = page.getByRole('button', { name: 'Steering', exact: true })
  const open = async (task: string) => {
    await sidebar.getByRole('button', { name: task }).click()
    await expect(page.getByRole('heading', { name: task, exact: true })).toBeVisible()
  }
  await open(tasks[0])
  await expect(steering).toBeVisible({ timeout: 60_000 })

  const left = Date.now()
  await open(tasks[1])
  await open(tasks[0])
  await expect(steering).toBeVisible()
  // Only inside the server's 15-second reconnect window does this prove the
  // tab reclaimed its own lease rather than acquiring an expired one.
  expect(Date.now() - left).toBeLessThan(15_000)
  await expect(page.getByRole('button', { name: 'Take control' })).toBeHidden()
  await expect(page.getByText('run control is held by another session')).toBeHidden()
})
