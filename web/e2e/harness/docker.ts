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
 * Removes containers by name, ignoring the ones that are already gone. A
 * cleanup failure must not mask the test's own result, so this never throws.
 */
export async function removeContainers(names: string[]): Promise<void> {
  if (names.length === 0) return
  try {
    await run('docker', ['rm', '-f', ...names], { timeout: 60_000 })
  } catch {
    // The whole call fails when any one name is missing, which is the normal
    // case for a run whose container the scheduler already destroyed. The
    // containers that do exist are still removed.
  }
}

/**
 * Removes the images `env.save` committed for these members. Saving an
 * environment is what the Agents step's confirmation does, so a suite that
 * did not clean up would leave one image per run behind.
 */
export async function removeMemberImages(memberIDs: string[]): Promise<void> {
  for (const id of memberIDs) {
    try {
      const { stdout } = await run(
        'docker',
        ['images', '--filter', `reference=aether/member-${id}`, '--format', '{{.ID}}'],
        { timeout: 60_000 },
      )
      const ids = stdout.split('\n').filter((line) => line !== '')
      if (ids.length > 0) await run('docker', ['rmi', '-f', ...ids], { timeout: 60_000 })
    } catch {
      // Cleanup only; a docker that will not answer is not this test's result.
    }
  }
}
