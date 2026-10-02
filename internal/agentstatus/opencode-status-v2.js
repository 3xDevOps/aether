// OpenCode @opencode/plugin 2.0.18. Separate from the mailbox plugin: this
// reports UI state only and never admits, reads, or acknowledges mailbox work.
import { spawn } from 'node:child_process'

const key = (session, kind, id) => JSON.stringify([session, kind, id])

// The 2.0.18 loader accepts { id, setup } directly; Plugin.define only returns
// its input. Avoid a runtime SDK dependency for the standalone mounted file.
export default {
  id: 'aether-status',
  setup(ctx) {
    const owner = Symbol.for('aether.opencode.status.v2')
    globalThis[owner]?.()
    const lifetime = new AbortController()
    const busy = new Set()
    const pending = new Map()
    let queue = Promise.resolve()
    let child
    let warned = false
    let executionKnown = false
    const warn = error => {
      if (warned || lifetime.signal.aborted) return
      warned = true
      console.error('[aether status reporter]', error.message ?? error)
    }
    const dispose = () => {
      lifetime.abort()
      child?.kill()
      process.off('exit', dispose)
    }
    globalThis[owner] = dispose
    process.once('exit', dispose)

    function report() {
      const working = busy.size > 0
      const body = JSON.stringify({
        ...(executionKnown ? { state: working ? 'working' : 'waiting' } : {}),
        ...(executionKnown && !working ? { reason: 'agent idle' } : {}),
        input_updates: [{ operation: 'replace', requests: [...pending.values()] }],
      })
      // Concurrent execution starts still refresh liveness when this body is unchanged.
      const args = ['report', 'opencode', '--json', body]
      queue = queue.then(() => {
        if (lifetime.signal.aborted) return
        return new Promise(resolve => {
          const running = spawn('/opt/aether/aether-server', args, { stdio: ['ignore', 'ignore', 'pipe'] })
          child = running
          running.stderr.setEncoding('utf8')
          running.stderr.on('data', chunk => {
            const message = chunk.split('\n')[0].trim()
            if (message) warn(message)
          })
          running.on('error', error => { warn(error); resolve() })
          running.on('close', () => {
            if (child === running) child = undefined
            resolve()
          })
        })
      }).catch(warn)
    }

    function open(session, kind, id) {
      if (typeof session !== 'string' || !session || typeof id !== 'string' || !id) return false
      pending.set(key(session, kind, id), { id, session_id: session, kind })
      return true
    }

    async function reconcilePermissions(sessionID) {
      const requests = [...pending].filter(([, request]) => request.kind === 'permission' && request.session_id === sessionID)
      if (!requests.length) return
      // Released 2.0.18 exposes permission.list, but no session.form.list.
      // Interrupted permission cleanup can omit permission.replied.
      const signal = AbortSignal.any([lifetime.signal, AbortSignal.timeout(4000)])
      const live = await ctx.permission.list({ sessionID }, { signal })
      if (lifetime.signal.aborted) return
      const ids = new Set(live.map(request => key(request.sessionID, 'permission', request.id)))
      for (const [id] of requests) if (!ids.has(id)) pending.delete(id)
    }

    void (async () => {
      for await (const event of ctx.event.subscribe({ signal: lifetime.signal })) {
        if (lifetime.signal.aborted) return
        if (event.location?.directory && event.location.directory !== ctx.location.directory) continue
        const data = event.data
        switch (event.type) {
          case 'location.shutdown':
            return dispose()
          case 'session.execution.started':
            if (!data.sessionID) break
            executionKnown = true
            busy.add(data.sessionID)
            report()
            break
          case 'session.execution.succeeded':
          case 'session.execution.failed':
          case 'session.execution.interrupted':
            if (!data.sessionID) break
            executionKnown = true
            busy.delete(data.sessionID)
            await reconcilePermissions(data.sessionID).catch(warn)
            if (lifetime.signal.aborted) return
            report()
            break
          case 'session.deleted':
            busy.delete(data.sessionID)
            for (const [id, request] of pending) if (request.session_id === data.sessionID) pending.delete(id)
            report()
            break
          case 'permission.asked':
            if (open(data.sessionID, 'permission', data.id)) report()
            break
          case 'permission.replied':
            if (!pending.delete(key(data.sessionID, 'permission', data.requestID))) break
            await reconcilePermissions(data.sessionID).catch(warn)
            report()
            break
          case 'form.created':
            if (open(data.form?.sessionID, 'form', data.form?.id)) report()
            break
          case 'form.replied':
          case 'form.cancelled':
            if (pending.delete(key(data.sessionID, 'form', data.id))) report()
            break
        }
      }
      if (!lifetime.signal.aborted) warn('OpenCode lifecycle stream ended')
    })().catch(warn)
    return dispose
  },
}
