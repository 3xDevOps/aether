// Container and image cleanup. The child server is the real binary, so its
// containers carry only the production `aether.managed` label - there is no
// test label to sweep on. They are addressed by the names the runtime derives
// instead: its `aether-run-` prefix over the scheduler's own container names,
// which are the run id for a run and `aether-terminal-<member-id>` for a
// member's environment.

import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const run = promisify(execFile)

const prefix = 'aether-run-'

export const runContainer = (runID: string) => prefix + runID
export const terminalContainer = (memberID: string) =>
  `${prefix}aether-terminal-${memberID}`

/**
 * Cleanup is best-effort - a docker that will not answer must not turn a
 * passing test red - but never silent: an unswept container or image is a
 * leak on the machine that ran the suite, and the warning is the only way
 * anyone finds out.
 */
function warn(what: string, err: unknown): void {
  console.warn(`e2e cleanup: ${what} failed: ${String(err)}`)
}

/**
 * Removes containers by name. `docker rm -f` ignores names that are already
 * gone, which is the normal case for a run whose container the scheduler
 * destroyed on exit, so a non-zero exit here is a real failure.
 */
export async function removeContainers(names: string[]): Promise<void> {
  if (names.length === 0) return
  try {
    await run('docker', ['rm', '-f', ...names], { timeout: 60_000 })
  } catch (err) {
    warn(`docker rm -f ${names.join(' ')}`, err)
  }
}

/**
 * Removes the images `env.save` committed for these members. Saving an
 * environment is what the Agents step's confirmation does, so a suite that
 * did not clean up would leave one image behind per run.
 */
export async function removeMemberImages(memberIDs: string[]): Promise<void> {
  for (const id of memberIDs) {
    const reference = `aether/member-${id}`
    try {
      const { stdout } = await run(
        'docker',
        ['images', '--filter', `reference=${reference}`, '--format', '{{.ID}}'],
        { timeout: 60_000 },
      )
      const ids = stdout.split('\n').filter((line) => line !== '')
      if (ids.length > 0) await run('docker', ['rmi', '-f', ...ids], { timeout: 60_000 })
    } catch (err) {
      warn(`removing ${reference}`, err)
    }
  }
}
