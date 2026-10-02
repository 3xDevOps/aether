// OpenCode V1 1.18.32 server plugin, not a V2 Plugin.define entrypoint.
// V1 prompt joins an active loop; it is NOT a next-turn queue. Wake only at idle.
import { execFile } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { setTimeout as delay } from 'node:timers/promises'

const MESSAGE_ID = 'msg_aether_mailbox_context'
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

export default async function ({ client, directory }) {
  const owner = process.env.AETHER_NATIVE_HOOK_OWNER
  if (owner && owner !== String(process.pid)) return {}
  const previous = globalThis[OWNER]
  if (previous && previous.version !== 1) throw new Error('Aether: do not co-load OpenCode V1 and V2 mailbox plugins')
  previous?.dispose()
  process.env.AETHER_NATIVE_HOOK_OWNER = String(process.pid)
  const state = {
    version: 1,
    root: previous?.root,
    notified: previous?.notified ?? new Map(),
    paused: previous?.paused ?? false,
    retired: previous?.retired ?? false,
    busy: previous?.busy ?? true,
    completed: previous?.completed ?? false,
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
    if (!current() || state.paused || !state.root || state.busy || !state.completed || receiver) return
    const controller = new AbortController()
    receiver = controller
    const epoch = generation
    const valid = () => current() && !controller.signal.aborted && generation === epoch && !state.paused
    void (async () => {
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
          // Only helper exit 1 is a recoverable transport failure. Never spin on
          // unsupported/denied/missing-run responses or malformed JSON.
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
        for (const id of state.notified.keys()) if (!unread.has(id)) state.notified.delete(id)
        waitSeconds = 30
        if (!result.wake_admitted || !result.context || !observed.some(id => !state.notified.has(id))) continue
        // Events are the primary idle boundary; the live status read closes
        // missed/delayed busy notifications without inventing a V1 queue option.
        const { data: statuses } = await client.session.status({ signal: controller.signal, throwOnError: true })
        if (!valid()) return
        if (statuses[state.root] && statuses[state.root].type !== 'idle') {
          state.busy = true
          state.completed = false
          return cancel()
        }
        await client.session.get({ path: { id: state.root }, signal: controller.signal, throwOnError: true })
        if (!valid()) return
        const token = wakeToken = randomUUID()
        wakeGeneration = epoch
        const dispatchIDs = observed.filter(id => !state.notified.has(id))
        // Reserve before awaiting: native lifecycle events can restart polling
        // before acceptance settles. A rejection releases only this request.
        for (const id of dispatchIDs) state.notified.set(id, token)
        state.busy = true
        state.completed = false
        // promptAsync accepts a request, not model receipt. The trusted pointer
        // is an ordinary synthetic user part; no peer body is promoted.
        try {
          await client.session.promptAsync({
            path: { id: state.root },
            body: { parts: [{ type: 'text', text: result.context, synthetic: true, metadata: { aetherMailbox: token } }] },
            signal: controller.signal,
            throwOnError: true,
          })
        } catch (error) {
          for (const id of dispatchIDs) if (state.notified.get(id) === token) state.notified.delete(id)
          throw error
        }
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

  start()

  return {
    async dispose() { dispose() },
    async 'chat.message'(input, output) {
      if (globalThis[OWNER] !== state) return
      const token = output?.parts.find(part => part.metadata?.aetherMailbox)?.metadata.aetherMailbox
      if (token !== undefined) {
        if (!current() || token !== wakeToken || wakeGeneration !== generation || state.paused) throw new Error('Aether mailbox wake was cancelled')
        state.busy = true
        state.completed = false
        return
      }
      if (!current()) return
      if (input.sessionID === state.root) {
        state.busy = true
        state.completed = false
        cancel()
      }
      // Serialize lookup so a faster sibling lookup cannot steal the first
      // explicitly prompted root. Children never select or replace the owner.
      const select = binding.then(async () => {
        if (!current()) return
        const { data: session } = await client.session.get({ path: { id: input.sessionID }, signal: lifetime.signal, throwOnError: true })
        if (!current() || session.parentID) return
        if (state.root && state.root !== input.sessionID) return retire()
        state.root ??= input.sessionID
        state.paused = false
        state.busy = true
        state.completed = false
        cancel()
      })
      binding = select.catch(report)
      await select
    },
    async event({ event }) {
      if (!current()) return
      const properties = event.properties
      if (event.type === 'server.instance.disposed' && properties.directory === directory) return dispose()
      if (event.type === 'session.deleted' && properties.info.id === state.root) return retire()
      if (event.type === 'message.updated' && properties.info.sessionID === state.root && properties.info.role === 'assistant') {
        const message = properties.info
        if (message.error) return pause()
        state.completed = Boolean(message.time.completed && message.finish && !['tool-calls', 'unknown'].includes(message.finish))
      }
      if (properties.sessionID !== state.root) return
      if (event.type === 'session.error') return pause()
      if (event.type === 'session.status' && properties.status.type !== 'idle') {
        // V1 emits busy again after publishing the terminal assistant, before
        // its final loop iteration discovers completion. That is not new work.
        if (!state.busy) state.completed = false
        state.busy = true
        return cancel()
      }
      if (event.type === 'session.idle' || (event.type === 'session.status' && properties.status.type === 'idle')) {
        state.busy = false
        // Shell/setup cancellation has no distinguishable abort event in V1.
        // Only a successful model completion rearms automatic idle work.
        if (state.completed) start()
      }
    },
    async 'experimental.chat.messages.transform'(_input, output) {
      const lastUser = output.messages.findLast(message => message.info.role === 'user' && message.info.id !== MESSAGE_ID)
      if (!current() || !lastUser || lastUser.info.sessionID !== state.root) return
      const epoch = generation
      let content
      try {
        content = await helper('context', {}, activity.signal)
      } catch (error) {
        if (!current() || epoch !== generation) return
        throw error
      }
      if (!current() || generation !== epoch || lastUser.info.sessionID !== state.root) return
      const messages = output.messages.filter(message => message.info.id !== MESSAGE_ID)
      if (content) messages.push({
        info: { ...lastUser.info, id: MESSAGE_ID, time: { created: Date.now() } },
        parts: [{
          id: 'prt_aether_mailbox_context', sessionID: state.root, messageID: MESSAGE_ID,
          type: 'text', text: content, synthetic: true,
        }],
      })
      // V1 consumes this array by reference; the context hint is not persisted.
      output.messages.splice(0, output.messages.length, ...messages)
    },
  }
}
