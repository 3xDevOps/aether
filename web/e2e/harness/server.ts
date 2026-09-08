// A real `aether-server` as a child process: real SQLite, real git
// transport, real Docker. Nothing here is stubbed except the agent, which
// is the scheduler's deterministic `fake` harness.

import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { binaries } from './paths'
import { type Child, portOpen, reservePort, start, waitFor } from './process'

/**
 * The image every container in this suite starts from, pinned to the tag
 * `internal/runtime`'s integration tests already use so a run of either
 * suite warms the other's pull. The published standard image is a large
 * download that proves nothing the scheduler path does not.
 */
const standardImage = 'busybox:1.36'

/**
 * The argv the `fake` harness runs, read from the server's environment at
 * every launch. `agent.sh` is committed to the seed repository and the run
 * checkout is bind-mounted at /workspace.
 */
const fakeAgent = 'sh /workspace/agent.sh'

/**
 * Whether a Docker daemon answers, the way the Go integration suite probes
 * for one. Synchronous so a spec file can skip itself at collection time
 * rather than after its fixtures have already started a server.
 */
export function dockerReachable(): boolean {
  try {
    execFileSync('docker', ['info', '--format', '{{.ServerVersion}}'], {
      timeout: 10_000,
      stdio: 'ignore',
    })
    return true
  } catch {
    return false
  }
}

export interface Server {
  /** host:port of the SSH transport, which is all the server listens on. */
  addr: string
  dataDir: string
  /** The member's environment home on the server, as the scheduler sees it. */
  memberHome: (memberID: string) => string
  output: () => string
  stop: () => Promise<void>
}

/**
 * Starts the server in `dir` and waits for its SSH listener. The first
 * identity to authenticate becomes the admin, so nothing is seeded here:
 * the wizard's own Link step bootstraps the account.
 */
export async function startServer(dir: string): Promise<Server> {
  const dataDir = path.join(dir, 'data')
  mkdirSync(dataDir, { recursive: true })
  // An explicit empty options file: without --config the server reads the
  // host's /etc file, and a developer machine that has one would change
  // what the suite tests.
  const configPath = path.join(dir, 'server-options')
  writeFileSync(configPath, '')

  const port = await reservePort()
  const addr = `127.0.0.1:${port}`
  const child: Child = start(
    binaries().server,
    [
      'serve',
      '--data-dir',
      dataDir,
      '--addr',
      addr,
      '--config',
      configPath,
      '--standard-image',
      standardImage,
    ],
    { ...process.env, AETHER_FAKE_AGENT: fakeAgent },
  )

  try {
    await waitFor(`aether-server on ${addr}`, () => portOpen(port))
  } catch (err) {
    await child.stop()
    throw new Error(`${(err as Error).message}\n${child.output()}`)
  }

  return {
    addr,
    dataDir,
    memberHome: (memberID) => path.join(dataDir, 'homes', memberID),
    output: child.output,
    stop: child.stop,
  }
}
