// The coordination CLI mounted in every real run container. Calling it over
// docker exec drives the same socket and admission path as an installed
// agent skill, without pretending to be one.

import { execFileSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import path from 'node:path'
import { expect } from '@playwright/test'
import { runContainer } from './docker'

export function runCoordCLI<T>(runID: string, args: string[], input?: string): T {
  const output = execFileSync(
    'docker',
    ['exec', '-i', runContainer(runID), 'aether-internal', ...args],
    { input, encoding: 'utf8', timeout: 60_000 },
  )
  const envelope = JSON.parse(output) as { ok?: boolean; result?: T; error?: { message?: string } }
  if (!envelope.ok || envelope.result === undefined) {
    throw new Error(envelope.error?.message || `aether-internal ${args.join(' ')} failed`)
  }
  return envelope.result
}

// The scheduler opens the socket before it creates the container that mounts
// it, so the CLI is reachable only once the container runs too.
export async function waitForCoordCLI(runID: string, dataDir: string): Promise<void> {
  await expect
    .poll(() => {
      if (!existsSync(path.join(dataDir, 'coord', runID, 'coord3.sock'))) return 'no coord3.sock yet'
      try {
        const running = execFileSync('docker', ['inspect', '--format', '{{.State.Running}}', runContainer(runID)], {
          encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 10_000,
        }).trim()
        return running === 'true' ? 'ready' : `container running=${running}`
      } catch (err) {
        return `docker inspect: ${(err as { stderr?: string }).stderr?.trim() || String(err)}`
      }
    }, { timeout: 30_000, intervals: [100, 250, 500] })
    .toBe('ready')
}
