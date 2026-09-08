// Where the suite finds the binaries it drives, and the scratch directory
// every run writes into.

import { existsSync, mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

/** The repository root: this file lives at web/e2e/harness/. */
const repoRoot = path.resolve(
  fileURLToPath(new URL('../../..', import.meta.url)),
)

export interface Binaries {
  /** `aether-server`, run as a child process on a loopback SSH port. */
  server: string
  /** `aether`, run as `aether gui` to serve the dashboard. */
  cli: string
}

/**
 * The binaries under test. `make test-e2e` builds them into `dist/` first,
 * because the CLI serves the SPA out of its own embedded `web/dist` and a
 * stale binary would test a stale dashboard.
 */
export function binaries(): Binaries {
  const dir = process.env.AETHER_E2E_BIN_DIR ?? path.join(repoRoot, 'dist')
  const found = {
    server: path.join(dir, 'aether-server'),
    cli: path.join(dir, 'aether'),
  }
  for (const file of Object.values(found)) {
    if (!existsSync(file)) {
      throw new Error(
        `${file} is missing; build it first with \`make test-e2e\` (or \`make build\`)`,
      )
    }
  }
  return found
}

/**
 * A scratch directory for one test. It is deliberately short and outside the
 * repository: the server opens a unix socket under its data directory, and a
 * path built from a test name overruns the 108-byte sun_path limit.
 */
export function scratchDir(): string {
  return mkdtempSync(path.join(tmpdir(), 'aether-e2e-'))
}
