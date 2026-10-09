// A real server as a child process: real SQLite, git and Docker. Most scenarios
// use the shipped binary and deterministic fake agent. GitHub journeys use a
// package-server test binary that replaces only external GitHub providers.

import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { binaries, repoRoot, scratchDir } from './paths'
import { seedRepo } from './git'
import { type Child, portOpen, reservePort, start, waitFor } from './process'

/**
 * The image every container in this suite starts from, pinned to the tag
 * `internal/runtime`'s integration tests already use so a run of either
 * suite warms the other's pull. The published standard image is a large
 * download that proves nothing the scheduler path does not.
 */
export const standardImage = 'busybox:1.36'

/**
 * The argv the `fake` harness runs, read from the server's environment at
 * every launch. `agent.sh` is committed to the seed repository and the run
 * checkout is bind-mounted at /workspace.
 */
const fakeAgent = 'sh /workspace/agent.sh'

/**
 * Whether the `docker` CLI can reach a daemon. This deliberately probes the
 * CLI rather than the API socket the server itself uses: teardown removes
 * the containers and images a test left behind by shelling out to `docker`,
 * so a host with the socket but no CLI cannot run this suite either way.
 * The same answer therefore gates both the container scenarios and the
 * cleanup.
 *
 * Synchronous so a spec file can skip itself at collection time rather than
 * after its fixtures have already started a server, and answered once per
 * worker because every teardown asks.
 */
let reachable: boolean | undefined

export function dockerReachable(): boolean {
  if (reachable === undefined) {
    try {
      execFileSync('docker', ['info', '--format', '{{.ServerVersion}}'], {
        timeout: 10_000,
        stdio: 'ignore',
      })
      reachable = true
    } catch {
      reachable = false
    }
  }
  return reachable
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

export interface ServerOptions {
  standardImage?: string
  /** Test-only GitHub HTTP/Git provider; the server remains real. */
  githubProvider?: boolean
}


let githubServerBinary: string | undefined

function githubProviderServer(): string {
  if (!githubServerBinary) {
    githubServerBinary = path.join(scratchDir(), 'github-provider-server.test')
    execFileSync('go', ['test', '-c', '-tags=integration', '-o', githubServerBinary, './internal/server'], {
      cwd: repoRoot,
      stdio: 'inherit',
    })
  }
  return githubServerBinary
}
/**
 * Starts the server in `dir` and waits for its SSH listener. The first
 * identity to authenticate becomes the admin, so nothing is seeded here:
 * the wizard's own Link step bootstraps the account.
 */
export async function startServer(dir: string, options: ServerOptions = {}): Promise<Server> {
  const dataDir = path.join(dir, 'data')
  mkdirSync(dataDir, { recursive: true })
  // An explicit empty options file: without --config the server reads the
  // host's /etc file, and a developer machine that has one would change
  // what the suite tests.
  const configPath = path.join(dir, 'server-options')
  writeFileSync(configPath, '')

  const port = await reservePort()
  const addr = `127.0.0.1:${port}`
  if (options.githubProvider) {
    await seedRepo(path.join(dir, 'repos'), 'first')
    const second = await seedRepo(path.join(dir, 'repos'), 'second')
    execFileSync('git', ['-C', second, 'branch', '-m', 'main', 'trunk'])
  }
  const child: Child = start(
    options.githubProvider ? githubProviderServer() : binaries().server,
    options.githubProvider ? ['-test.run=^TestGitHubBrowserProviderHarness$', '-test.timeout=0'] : [
      'serve',
      '--data-dir',
      dataDir,
      '--addr',
      addr,
      '--config',
      configPath,
      '--standard-image',
      options.standardImage ?? standardImage,
    ],
    {
      ...process.env,
      AETHER_FAKE_AGENT: fakeAgent,
      ...(options.githubProvider ? {
        AETHER_E2E_GITHUB_ROOT: dir,
        AETHER_E2E_GITHUB_ADDR: addr,
        AETHER_E2E_GITHUB_IMAGE: options.standardImage ?? standardImage,
      } : {}),
    },
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
