// OpenCode V2 @opencode/plugin API (official V2 docs, 2026-09-26).
// Not compatible with V1. One intended root per plugin lifetime; reload to rebind.
import { execFile } from 'node:child_process'
import { Plugin } from '@opencode/plugin'

function pendingContext() {
  const { promise, resolve, reject } = Promise.withResolvers()
  const child = execFile('/usr/local/bin/aether-internal', ['hook', 'opencode', 'context'], (error, stdout) => {
    if (error) reject(error)
    else resolve(stdout.trim())
  })
  child.stdin.on('error', reject)
  child.stdin.end('{}')
  return promise
}

export default Plugin.define({
  id: 'aether-mailbox',
  async setup(ctx) {
    const owner = process.env.AETHER_NATIVE_HOOK_OWNER
    if (owner && owner !== String(process.pid)) return
    process.env.AETHER_NATIVE_HOOK_OWNER = String(process.pid)
    let rootSessionID

    await ctx.session.hook('prompt', async event => {
      if (rootSessionID !== undefined) return
      const session = await ctx.session.get({ sessionID: event.sessionID })
      if (!session.parentID && rootSessionID === undefined) rootSessionID = event.sessionID
    })
    await ctx.session.hook('context', async event => {
      if (rootSessionID === undefined || event.sessionID !== rootSessionID) return
      const content = await pendingContext()
      // V2 owns a fresh request draft; context hooks exclude title, compaction,
      // and transient generate calls. Only the trusted helper instruction is
      // system context; the agent reads untrusted peer bodies through inbox.
      if (content) event.system.push({ type: 'text', text: content })
    })
  },
})
