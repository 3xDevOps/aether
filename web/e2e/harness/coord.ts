// The coordination CLI mounted in every real run container. Calling it over
// docker exec drives the same socket and admission path as an installed
// integrator or worker skill, without pretending to be one.

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

export async function waitForCoordCLI(runID: string, dataDir: string): Promise<void> {
  await expect
    .poll(() => existsSync(path.join(dataDir, 'coord', runID, 'coord3.sock')), {
      timeout: 30_000,
      intervals: [100, 250, 500],
    })
    .toBeTruthy()
}
