// Child processes, loopback ports, and the polling both readiness checks
// need. Everything here is generic; the server and the gateway build on it.

import { spawn } from 'node:child_process'
import net from 'node:net'

/**
 * A free loopback port, released before it is handed back. The server and
 * the gateway are both told which port to bind rather than being asked
 * afterwards, so a restart can claim the same address.
 *
 * Closing the probe before the caller binds leaves a window something else
 * could take the port in. Nothing does today: `playwright.config.ts` runs
 * one worker, and each CI job has the runner to itself. Raising the worker
 * count reopens that window, so pass the listener on instead of the number.
 */
export function reservePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = net.createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address()
      if (address === null || typeof address === 'string') {
        probe.close()
        reject(new Error('reservePort: the probe listener has no TCP address'))
        return
      }
      probe.close(() => resolve(address.port))
    })
  })
}

/** Whether something accepts a TCP connection on this loopback port. */
export function portOpen(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = net.connect({ host: '127.0.0.1', port })
    const done = (open: boolean) => {
      socket.destroy()
      resolve(open)
    }
    socket.setTimeout(1000)
    socket.once('connect', () => done(true))
    socket.once('timeout', () => done(false))
    socket.once('error', () => done(false))
  })
}

const sleep = (ms: number) =>
  new Promise((resolve) => setTimeout(resolve, ms))

/**
 * Polls `ready` until it answers true. `what` names the thing being waited
 * for so a timeout says which process never came up.
 */
export async function waitFor(
  what: string,
  ready: () => Promise<boolean>,
  timeoutMs = 60_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    if (await ready()) return
    if (Date.now() > deadline) {
      throw new Error(`${what} was not ready after ${timeoutMs}ms`)
    }
    await sleep(200)
  }
}

export interface Child {
  /** Everything the process has written to stdout and stderr so far. */
  output: () => string
  /** SIGTERM, then SIGKILL if it is still there, and wait for the exit. */
  stop: () => Promise<void>
}

/**
 * Starts a child process and captures its output. The output is what a
 * failed readiness check reports, so nothing the process said is lost.
 */
export function start(
  command: string,
  args: string[],
  env: NodeJS.ProcessEnv,
  cwd?: string,
): Child {
  const child = spawn(command, args, {
    cwd,
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let captured = ''
  child.stdout.setEncoding('utf8')
  child.stderr.setEncoding('utf8')
  child.stdout.on('data', (chunk: string) => {
    captured += chunk
  })
  child.stderr.on('data', (chunk: string) => {
    captured += chunk
  })

  const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()))
  return {
    output: () => captured,
    stop: async () => {
      if (child.exitCode !== null || child.signalCode !== null) return
      child.kill('SIGTERM')
      const killer = setTimeout(() => child.kill('SIGKILL'), 10_000)
      await exited
      clearTimeout(killer)
    },
  }
}
