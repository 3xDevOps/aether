// The `aether` fixture: one real server and as many real gateways as a
// scenario has members, torn down with everything they created.
//
// Every test gets its own server, its own loopback ports and its own scratch
// directory, so the suite has no shared state to order tests around.

import { execFileSync } from 'node:child_process'
import { copyFileSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { test as base } from '@playwright/test'

import { GatewayClient, type InviteResult } from './harness/client'
import {
  reclaimOwnership,
  removeContainers,
  removeMemberImages,
  runContainer,
  terminalContainer,
} from './harness/docker'
import { type Gateway, startGateway } from './harness/gateway'
import { cloneRepo, seedRepo } from './harness/git'
import { repoRoot, scratchDir } from './harness/paths'
import { dockerReachable, type Server, type ServerOptions, standardImage, startServer } from './harness/server'

export interface Member {
  name: string
  /** The tokened dashboard URL this member's browser opens. */
  url: string
  /** This member's machine: SSH key, known_hosts, agent configuration. */
  home: string
  /** The same gateway the browser talks to, for setup a test is not about. */
  api: GatewayClient
}

export interface Aether {
  server: Server
  /** Another machine with its own gateway, i.e. another member joining. */
  member: (name: string) => Promise<Member>
  /** A repository with one commit on `main` and the fake agent's script. */
  seedRepo: (name: string) => Promise<string>
  cloneRepo: (source: string, name: string) => Promise<string>
  /** Mints an invite code, which only an admin may do. */
  invite: (admin: Member) => Promise<string>
  /**
   * Puts an executable in a member's environment home on the server. That
   * directory is bind-mounted into their environment container, and it is
   * where agent.list looks to decide whether an agent is installed. `script`
   * is the shell body it runs, for a scenario that launches it; the default
   * does nothing, which is all a detection test needs.
   */
  installAgent: (memberID: string, executable: string, script?: string) => void
  /**
   * Writes a git identity into a member's own machine, which is what the
   * Git identity step offers as the default.
   */
  giveGitIdentity: (member: Member, name: string, email: string) => void
  /**
   * Puts the stub `gh` in a member's environment home. GitHub itself is not
   * under test here; what the connect path does around it is.
   */
  installStubGh: (memberID: string) => void
  /**
   * Puts a stub `npm` in a member's environment home, so `agent.install`
   * runs the real install command for codex and its adapter without a
   * registry. It records each call and writes the executable the package
   * would link.
   */
  installStubNpm: (memberID: string) => void
  /** Writes the file agent.list reads as a member's codex login. */
  giveCodexLogin: (memberID: string) => void
  /**
   * Builds the acpmock agent (internal/acphost/acpmock/agent) and registers
   * it as the member agent `mock`, whose Enhanced mode runs it in the run
   * container. Prompts it understands: `demo`, `ask permission`, `ask
   * form`, `wait`, `refuse`.
   */
  installACPMock: (member: Member, memberID: string) => Promise<void>
}

let acpMock: string | undefined

/** Static, so it runs in the busybox image; built once per worker. */
function buildACPMock(): string {
  if (!acpMock) {
    acpMock = path.join(scratchDir(), 'acp-mock')
    execFileSync('go', ['build', '-o', acpMock, './internal/acphost/acpmock/agent'], {
      cwd: repoRoot,
      env: { ...process.env, CGO_ENABLED: '0' },
      stdio: 'inherit',
    })
  }
  return acpMock
}

/**
 * A stub `gh` for the environment container: busybox `sh`, so no bashisms.
 * It answers exactly the login a member types and the three invocations
 * `github.connect` makes, records every call so the spec can assert the
 * argv, and fails loudly on anything else rather than passing silently when
 * an invocation changes. `auth setup-git` writes the credential block real
 * gh writes, which is the shape the server's own `.gitconfig` edits must
 * leave alone.
 */
const stubGh = `#!/bin/sh
echo "$*" >> "$HOME/gh-calls.log"
if [ "$1" = "--version" ]; then
	echo "gh version 2.100.0 (2026-09-03)"
	exit 0
fi
case "$1 $2" in
"auth login")
	echo "! First copy your one-time code: ABCD-1234"
	;;
"auth status")
	echo '{"hosts":{"github.com":[{"state":"success","active":true,"login":"octocat","scopes":"admin:ssh_signing_key, gist, read:org, repo"}]}}'
	;;
"auth setup-git")
	printf '[credential "https://github.com"]\\n\\thelper = !gh auth git-credential\\n' >> "$HOME/.gitconfig"
	;;
"ssh-key add")
	cp "$3" "$HOME/gh-registered-key"
	;;
"ssh-key list")
	# The SHA256 fingerprint of the registered key, the way ssh-keygen -l
	# prints it: base64 of the digest of the decoded key blob, unpadded.
	# busybox has no ssh-keygen, so the digest is spelled out.
	hex=$(awk '{print $2}' "$HOME/gh-registered-key" | base64 -d | sha256sum | cut -d' ' -f1)
	fp=$(printf "$(printf %s "$hex" | sed 's/../\\\\x&/g')" | base64 | tr -d '=\n')
	printf 'aether\tSHA256:%s\t2026-01-01T00:00:00Z\t1\tsigning\n' "$fp"
	;;
*)
	echo "gh: unsupported invocation: $*" >&2
	exit 1
	;;
esac
`

/** busybox `sh`; fails loudly on a package it does not know. */
const stubNpm = `#!/bin/sh
echo "$*" >> "$HOME/npm-calls.log"
for a; do
	case "$a" in
	@openai/codex) bin=codex ;;
	@agentclientprotocol/codex-acp@*) bin=codex-acp ;;
	esac
done
if [ -z "$bin" ]; then
	echo "npm: unsupported install: $*" >&2
	exit 1
fi
mkdir -p "$HOME/.local/bin"
printf '#!/bin/sh\\n' > "$HOME/.local/bin/$bin"
chmod +x "$HOME/.local/bin/$bin"
echo "added 1 package ($bin)"
`

/**
 * What this test's server may still own in Docker, asked for before the
 * server stops because only the server can say which members and runs exist.
 */
async function leftovers(
  gateways: Gateway[],
): Promise<{ containers: string[]; memberIDs: string[] }> {
  for (const gateway of gateways) {
    const api = new GatewayClient(gateway.addr, gateway.token)
    try {
      const { members } = await api.rpc<{ members: { id: string }[] }>('member.list')
      const { runs } = await api.rpc<{ runs: { id: string }[] }>('run.list')
      return {
        containers: [
          ...members.map((m) => terminalContainer(m.id)),
          ...runs.map((r) => runContainer(r.id)),
        ],
        memberIDs: members.map((m) => m.id),
      }
    } catch {
      // An unlinked gateway has no server to ask; try the next one.
    }
  }
  return { containers: [], memberIDs: [] }
}

export const test = base.extend<{ aether: Aether; serverOptions: ServerOptions }>({
  serverOptions: [{}, { option: true }],
  aether: async ({ serverOptions }, use, testInfo) => {
    const dir = scratchDir()
    const server = await startServer(dir, serverOptions)
    const gateways: Gateway[] = []

    const member = async (name: string): Promise<Member> => {
      const gateway = await startGateway(dir, name)
      gateways.push(gateway)
      return {
        name,
        url: gateway.url,
        home: gateway.home,
        api: new GatewayClient(gateway.addr, gateway.token),
      }
    }

    await use({
      server,
      member,
      seedRepo: (name) => seedRepo(path.join(dir, 'repos'), name),
      cloneRepo: (source, name) => cloneRepo(path.join(dir, 'repos'), source, name),
      invite: async (admin) =>
        (await admin.api.rpc<InviteResult>('member.invite')).code,
      giveGitIdentity: (member, name, email) => {
        writeFileSync(
          path.join(member.home, '.gitconfig'),
          `[user]\n\tname = ${name}\n\temail = ${email}\n`,
        )
      },
      installAgent: (memberID, executable, script = '') => {
        const bin = path.join(server.memberHome(memberID), '.local', 'bin')
        mkdirSync(bin, { recursive: true })
        writeFileSync(path.join(bin, executable), `#!/bin/sh\n${script}\n`, {
          mode: 0o755,
        })
      },
      installStubGh: (memberID) => {
        // The environment terminal's PATH puts ~/.local/bin first, and
        // docker exec inherits it, so both halves of the connect reach this.
        const bin = path.join(server.memberHome(memberID), '.local', 'bin')
        mkdirSync(bin, { recursive: true })
        writeFileSync(path.join(bin, 'gh'), stubGh, { mode: 0o755 })
      },
      installStubNpm: (memberID) => {
        const bin = path.join(server.memberHome(memberID), '.local', 'bin')
        mkdirSync(bin, { recursive: true })
        writeFileSync(path.join(bin, 'npm'), stubNpm, { mode: 0o755 })
      },
      installACPMock: async (member, memberID) => {
        const bin = path.join(server.memberHome(memberID), '.local', 'bin')
        mkdirSync(bin, { recursive: true })
        copyFileSync(buildACPMock(), path.join(bin, 'acp-mock'))
        await member.api.rpc('agent.register', {
          definition: { name: 'mock', executable: 'acp-mock', tui_args: ['acp-mock'], headless_args: ['acp-mock'], acp_args: ['acp-mock'] },
        })
      },
      giveCodexLogin: (memberID) => {
        const dir = path.join(server.memberHome(memberID), '.codex')
        mkdirSync(dir, { recursive: true })
        writeFileSync(path.join(dir, 'auth.json'), '{}\n')
      },
    })

    const { containers, memberIDs } = await leftovers(gateways)
    await Promise.all(gateways.map((g) => g.stop()))
    await server.stop()
    // A scenario that never needed Docker left nothing in it, and the
    // sweep would only report a missing CLI it never used.
    if (dockerReachable()) {
      await removeContainers(containers)
      await removeMemberImages(memberIDs)
    }
    // A failed test keeps its data directory, server log and repositories.
    if (testInfo.status === testInfo.expectedStatus) {
      try {
        rmSync(dir, { recursive: true, force: true })
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code !== 'EACCES') throw err
        await reclaimOwnership(dir, serverOptions.standardImage ?? standardImage)
        rmSync(dir, { recursive: true, force: true })
      }
    } else {
      await testInfo.attach('aether-server output', { body: server.output() })
    }
  },
})

export { expect } from '@playwright/test'
