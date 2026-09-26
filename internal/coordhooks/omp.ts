// oh-my-pi 18.3.1 extension; load once with -e or from its extensions directory.
// Its injected SDK registry identifies Main in TUI/RPC/print, including reloads.
import { execFile } from 'node:child_process'
import type { ExtensionAPI } from '@oh-my-pi/pi-coding-agent'

const CUSTOM_TYPE = 'aether-mailbox-pending'

function pendingContext() {
  const { promise, resolve, reject } = Promise.withResolvers<string>()
  const child = execFile('/usr/local/bin/aether-internal', ['hook', 'omp', 'context'], (error, stdout) => {
    if (error) reject(error)
    else resolve(stdout.trim())
  })
  child.stdin?.on('error', reject)
  child.stdin?.end('{}')
  return promise
}

export default function (omp: ExtensionAPI) {
  const owner = process.env.AETHER_NATIVE_HOOK_OWNER
  if (owner && owner !== String(process.pid)) return
  process.env.AETHER_NATIVE_HOOK_OWNER = String(process.pid)

  omp.on('context', async (event, ctx) => {
    const main = omp.pi.AgentRegistry.global().get(omp.pi.MAIN_AGENT_ID)
    if (main?.session?.sessionManager !== ctx.sessionManager) return
    const sessionID = ctx.sessionManager.getSessionId()
    const content = await pendingContext()
    if (sessionID !== ctx.sessionManager.getSessionId() ||
        main !== omp.pi.AgentRegistry.global().get(omp.pi.MAIN_AGENT_ID)) return

    // Return a fresh per-call array; never append to retained session history.
    // Custom content becomes developer context: the helper emits only a trusted
    // pending-mail instruction, never the untrusted peer message itself.
    const messages = event.messages.filter(message =>
      message.role !== 'custom' || message.customType !== CUSTOM_TYPE)
    if (content) messages.push({
      role: 'custom',
      customType: CUSTOM_TYPE,
      content,
      attribution: 'agent',
      display: false,
      timestamp: Date.now(),
    })
    return { messages }
  })
}
