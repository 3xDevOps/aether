// The `aether` fixture: one real server and as many real gateways as a
// scenario has members, torn down with everything they created.
//
// Every test gets its own server, its own loopback ports and its own scratch
// directory, so the suite has no shared state to order tests around.

import { cpSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { test as base } from '@playwright/test'

import { GatewayClient, type InviteResult } from './harness/client'
import {
  removeContainers,
  removeMemberImages,
  runContainer,
  terminalContainer,
} from './harness/docker'
import { type Gateway, startGateway } from './harness/gateway'
import { cloneRepo, seedRepo } from './harness/git'
import { scratchDir } from './harness/paths'
import { type Server, startServer } from './harness/server'

const testdata = path.resolve(fileURLToPath(new URL('./testdata', import.meta.url)))

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
   * Copies the fixture Claude Code configuration onto a member's machine,
   * where profile.preview and profile.push read it.
   */
  giveClaudeProfile: (member: Member) => void
  /**
   * Puts an executable in a member's environment home on the server. That
   * directory is bind-mounted into their environment container, and it is
   * where agent.list looks to decide whether an agent is installed.
   */
  installAgent: (memberID: string, executable: string) => void
}

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

export const test = base.extend<{ aether: Aether }>({
  aether: async ({}, use, testInfo) => {
    const dir = scratchDir()
    const server = await startServer(dir)
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
      giveClaudeProfile: (m) => {
        cpSync(path.join(testdata, 'claude-profile'), path.join(m.home, '.claude'), {
          recursive: true,
        })
      },
      installAgent: (memberID, executable) => {
        const bin = path.join(server.memberHome(memberID), '.local', 'bin')
        mkdirSync(bin, { recursive: true })
        writeFileSync(path.join(bin, executable), '#!/bin/sh\n', { mode: 0o755 })
      },
    })

    const { containers, memberIDs } = await leftovers(gateways)
    await Promise.all(gateways.map((g) => g.stop()))
    await server.stop()
    await removeContainers(containers)
    await removeMemberImages(memberIDs)
    // A failed test keeps its data directory, server log and repositories.
    if (testInfo.status === testInfo.expectedStatus) {
      rmSync(dir, { recursive: true, force: true })
    } else {
      await testInfo.attach('aether-server output', { body: server.output() })
    }
  },
})

export { expect } from '@playwright/test'
