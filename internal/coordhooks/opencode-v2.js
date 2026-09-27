// OpenCode V2 @opencode/plugin 2.0.18. Never load alongside opencode-v1.js.
// Wait for successful completion before admitting wake: Stop's interrupted
// event is published after cleanup, too late to safely enqueue during cleanup.
import { execFile } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { setTimeout as delay } from 'node:timers/promises'

const OWNER = Symbol.for('aether.opencode.mailbox')

function helper(command, input, signal) {
  return new Promise((resolve, reject) => {
    const child = execFile('/usr/local/bin/aether-internal', ['hook', 'opencode', command],
      { signal, timeout: command === 'wake' ? 35000 : 4000, maxBuffer: 262144 }, (error, stdout, stderr) => {
        if (error) {
          error.message = stderr.trim() || error.message
          reject(error)
        } else resolve(stdout.trim())
      })
    child.stdin.on('error', reject)
    child.stdin.end(JSON.stringify(input))
  })
}

// Plugin.define is a typing-only identity in 2.0.18. The native loader accepts
// this definition directly, so a copied read-only file needs no SDK package.
export default {
  id: 'aether-mailbox',
  async setup(ctx) {
    const owner = process.env.AETHER_NATIVE_HOOK_OWNER
    if (owner && owner !== String(process.pid)) return
    const previous = globalThis[OWNER]
    if (previous && previous.version !== 2) throw new Error('Aether: do not co-load OpenCode V1 and V2 mailbox plugins')
    previous?.dispose()
    process.env.AETHER_NATIVE_HOOK_OWNER = String(process.pid)
    const state = {
      version: 2,
      root: previous?.root,
      notified: previous?.notified ?? new Set(),
      paused: previous?.paused ?? false,
      retired: previous?.retired ?? false,
      busy: previous?.busy ?? true,
      dispose,
    }
    globalThis[OWNER] = state
    const lifetime = new AbortController()
    let activity = new AbortController()
    let receiver
    let generation = 0
    let observed = []
    let wakeToken
    let wakeGeneration
    let binding = Promise.resolve()
    const current = () => globalThis[OWNER] === state && !lifetime.signal.aborted && !state.retired
    const report = error => console.error('[aether mailbox]', error.message ?? error)

    function cancel() {
      generation++
      receiver?.abort()
      receiver = undefined
      activity.abort()
      activity = new AbortController()
    }
    function pause() {
      state.paused = true
      cancel()
    }
    function retire() {
      state.retired = true
      cancel()
    }
    function dispose() {
      cancel()
      lifetime.abort()
      process.off('exit', dispose)
    }
    process.once('exit', dispose)

    function start() {
      if (!current() || state.paused || !state.root || state.busy || receiver) return
      const controller = new AbortController()
      receiver = controller
      const epoch = generation
      const valid = () => current() && !controller.signal.aborted && generation === epoch && !state.paused
      void (async () => {
        // execution.succeeded precedes coordinator settlement. Do not let its
        // cleanup window become an accidental restart after a human Stop.
        await ctx.session.wait({ sessionID: state.root }, { signal: controller.signal })
        if (!valid()) return
        const session = await ctx.session.get({ sessionID: state.root }, { signal: controller.signal })
        if (!valid()) return
        if (session.parentID || session.time.archived || session.location.directory !== ctx.location.directory) return retire()
        if (session.outcome !== 'succeeded') return pause()
        let failures = 0
        let waitSeconds = 0
        while (valid()) {
          let result
          try {
            result = JSON.parse(await helper('wake', { seen_message_ids: observed, wait_seconds: waitSeconds }, controller.signal))
            failures = 0
          } catch (error) {
            if (!valid()) return
            report(error)
            if (error.code !== 1 || ++failures > 5) return pause()
            await delay(Math.min(1000 * 2 ** (failures - 1), 16000), undefined, { signal: controller.signal })
            continue
          }
          if (!valid()) return
          if (result.wait_supported !== true) {
            report(new Error('native mailbox waiting is unsupported; use aether-internal inbox'))
            return pause()
          }
          observed = result.unread_message_ids ?? []
          const unread = new Set(observed)
          for (const id of state.notified) if (!unread.has(id)) state.notified.delete(id)
          waitSeconds = 30
          if (!result.wake_admitted || !result.context || !observed.some(id => !state.notified.has(id))) continue
          const live = await ctx.session.get({ sessionID: state.root }, { signal: controller.signal })
          if (!valid()) return
          if (live.time.archived || live.location.directory !== ctx.location.directory) return retire()
          if (live.outcome === 'interrupted' || live.outcome === 'failed') return pause()
          wakeToken = randomUUID()
          wakeGeneration = epoch
          for (const id of observed) state.notified.add(id)
          state.busy = true
          // Actual 2.0.18 API: text, delivery, metadata; not V1 body.parts.
          // Queue protects a race into busy; it does not steer active work.
          await ctx.session.prompt({
            sessionID: state.root,
            text: result.context,
            delivery: 'queue',
            metadata: { aetherMailbox: wakeToken },
          }, { signal: controller.signal })
          return
        }
      })().catch(error => {
        if (!valid()) return
        report(error)
        pause()
      }).finally(() => {
        if (receiver === controller) {
          receiver = undefined
          if (valid() && !state.busy) start()
        }
      })
    }

    void (async () => {
      for await (const event of ctx.event.subscribe({ signal: lifetime.signal })) {
        if (!current()) continue
        if (event.type === 'location.shutdown' && event.location?.directory === ctx.location.directory) return dispose()
        if (event.data.sessionID !== state.root) continue
        if (event.type === 'session.deleted' || event.type === 'session.moved') {
          retire()
        } else if (event.type === 'session.execution.interrupted' || event.type === 'session.execution.failed') {
          pause()
        } else if (event.type === 'session.execution.started') {
          state.busy = true
          cancel()
        } else if (event.type === 'session.execution.succeeded') {
          state.busy = false
          start()
        }
      }
      if (current()) throw new Error('OpenCode lifecycle stream ended; mailbox wake paused')
    })().catch(error => {
      if (!current()) return
      report(error)
      pause()
    })

    await ctx.session.hook('prompt', async event => {
      if (globalThis[OWNER] !== state) return
      const token = event.metadata?.aetherMailbox
      if (token !== undefined) {
        if (!current() || token !== wakeToken || wakeGeneration !== generation || state.paused) throw new Error('Aether mailbox wake was cancelled')
        return
      }
      if (!current()) return
      if (event.sessionID === state.root) {
        state.busy = true
        cancel()
      }
      const select = binding.then(async () => {
        if (!current()) return
        const session = await ctx.session.get({ sessionID: event.sessionID }, { signal: lifetime.signal })
        if (!current() || session.parentID) return
        if (state.root && state.root !== event.sessionID) return retire()
        state.root ??= event.sessionID
        state.paused = false
        state.busy = true
        cancel()
      })
      binding = select.catch(report)
      await select
    }).catch(error => {
      dispose()
      throw error
    })
    await ctx.session.hook('context', async event => {
      if (!current() || event.sessionID !== state.root) return
      const epoch = generation
      let content
      try {
        content = await helper('context', {}, activity.signal)
      } catch (error) {
        if (!current() || epoch !== generation) return
        throw error
      }
      if (!current() || epoch !== generation || event.sessionID !== state.root) return
      // Only the canonical helper pointer is system context. Peer bodies stay
      // ordinary attributed inbox data and acknowledgement remains explicit.
      if (content) event.system.push({ type: 'text', text: content })
    }).catch(error => {
      dispose()
      throw error
    })
    start()
    return dispose
  },
}
