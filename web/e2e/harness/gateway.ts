// A real `aether gui` per member: the loopback HTTP gateway that serves the
// dashboard and proxies every call over that member's own SSH connection.
//
// Each gateway gets its own HOME and AETHER_CONFIG_DIR so the SSH key it
// generates, the known_hosts entry it writes, the link config it saves and
// the agent profile it scans all belong to one member and never touch the
// developer's own.

import { mkdirSync } from 'node:fs'
import path from 'node:path'

import { binaries } from './paths'
import { type Child, reservePort, start, waitFor } from './process'

/** One line of JSON on stdout: the contract `aether gui --json` publishes. */
interface Handshake {
  url: string
  addr: string
}

export interface Gateway {
  /** The tokened URL a browser opens: http://127.0.0.1:<port>/?token=<token> */
  url: string
  addr: string
  token: string
  /** This member's home directory: SSH key, known_hosts, agent profiles. */
  home: string
  output: () => string
  stop: () => Promise<void>
}

function gatewayEnv(home: string, configDir: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    HOME: home,
    SSH_AUTH_SOCK: '',
    USERPROFILE: home,
    AETHER_CONFIG_DIR: configDir,
    // Git identity probes run from this process. Pin every file-backed config
    // source so the host's global or system identity cannot leak in.
    GIT_CONFIG_GLOBAL: path.join(home, '.gitconfig'),
    GIT_CONFIG_NOSYSTEM: '1',
    // env.harnesses widens PATH from the login shell before it reports which
    // agents are installed on this machine. A fixed shell and PATH keep that
    // answer the same on a developer's laptop and on a CI runner.
    SHELL: '/bin/sh',
    PATH: '/usr/bin:/bin',
    // OpenSSH resolves ~ from the password database, not from HOME, so git
    // would otherwise look for this member's key and known_hosts in the real
    // user's home. Both files are the ones the Link step writes.
    GIT_SSH_COMMAND: [
      'ssh',
      `-i ${path.join(home, '.ssh', 'id_ed25519')}`,
      '-o IdentitiesOnly=yes',
      `-o UserKnownHostsFile=${path.join(home, '.ssh', 'known_hosts')}`,
      '-o BatchMode=yes',
    ].join(' '),
  }
}

/**
 * Starts a gateway for one member. It starts unlinked, which is the state
 * that sends the dashboard straight into the onboarding wizard.
 */
export async function startGateway(dir: string, name: string): Promise<Gateway> {
  const home = path.join(dir, name, 'home')
  const configDir = path.join(dir, name, 'config')
  mkdirSync(home, { recursive: true })
  mkdirSync(configDir, { recursive: true })

  const port = await reservePort()
  const child: Child = start(
    binaries().cli,
    ['gui', '--json', '--port', String(port)],
    gatewayEnv(home, configDir),
    home,
  )

  const firstLine = () => child.output().split('\n')[0] ?? ''
  try {
    await waitFor(`aether gui for ${name}`, async () => firstLine().startsWith('{'))
  } catch (err) {
    await child.stop()
    throw new Error(`${(err as Error).message}\n${child.output()}`)
  }
  const handshake = JSON.parse(firstLine()) as Handshake

  return {
    url: handshake.url,
    addr: handshake.addr,
    token: new URL(handshake.url).searchParams.get('token') ?? '',
    home,
    output: child.output,
    stop: child.stop,
  }
}
