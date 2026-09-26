// Native pi 0.87.1 extension; load once with -e or from its extensions directory.
// Context is per model call, not a persisted message or an idle wake-up.
import { execFile } from 'node:child_process'
import type { ExtensionAPI, ExtensionContext, SessionShutdownEvent } from '@earendil-works/pi-coding-agent'

interface MailboxScope {
  manager: ExtensionContext['sessionManager'] | null
  active: boolean
  replacement: (Pick<SessionShutdownEvent, 'reason' | 'targetSessionFile'> & {
    previousSessionFile: string | undefined
  }) | null
  generation: number
}

declare global {
  var __aetherPiMailboxScope: MailboxScope | undefined
}

const CUSTOM_TYPE = 'aether-mailbox-pending'

function pendingContext() {
  const { promise, resolve, reject } = Promise.withResolvers<string>()
  const child = execFile('/usr/local/bin/aether-internal', ['hook', 'pi', 'context'], (error, stdout) => {
    if (error) reject(error)
    else resolve(stdout.trim())
  })
  child.stdin?.on('error', reject)
  child.stdin?.end('{}')
  return promise
}

export default function (pi: ExtensionAPI) {
  const owner = process.env.AETHER_NATIVE_HOOK_OWNER
  if (owner && owner !== String(process.pid)) return
  process.env.AETHER_NATIVE_HOOK_OWNER = String(process.pid)
  // Native pi has one root runtime but no public main/subagent discriminator.
  // Bind its first session, then follow only the owner's explicit replacement.
  // Independent multi-root SDK hosts need their own recipient selection.
  const scope: MailboxScope = globalThis.__aetherPiMailboxScope ??=
    { manager: null, active: false, replacement: null, generation: 0 }

  pi.on('session_start', (event, ctx) => {
    const replacement = scope.replacement
    if (scope.manager && scope.manager !== ctx.sessionManager &&
        !(replacement && event.reason === replacement.reason &&
          event.previousSessionFile === replacement.previousSessionFile &&
          ctx.sessionManager.getSessionFile() === replacement.targetSessionFile)) return
    scope.manager = ctx.sessionManager
    scope.active = true
    scope.replacement = null
    scope.generation++
  })
  pi.on('session_shutdown', (event, ctx) => {
    if (scope.manager !== ctx.sessionManager) return
    scope.active = false
    scope.replacement = ['new', 'resume', 'fork'].includes(event.reason) ? {
      reason: event.reason,
      previousSessionFile: ctx.sessionManager.getSessionFile(),
      targetSessionFile: event.targetSessionFile,
    } : null
  })

  pi.on('context', async (event, ctx) => {
    const sessionManager = ctx.sessionManager
    if (!scope.active || scope.manager !== sessionManager) return
    const generation = scope.generation
    const content = await pendingContext()
    if (!scope.active || scope.manager !== sessionManager || scope.generation !== generation) return

    const messages = event.messages.filter(message =>
      message.role !== 'custom' || message.customType !== CUSTOM_TYPE)
    if (content) messages.push({
      role: 'custom',
      customType: CUSTOM_TYPE,
      content,
      display: false,
      timestamp: Date.now(),
    })
    return { messages }
  })
}
