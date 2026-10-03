// OpenCode @opencode/plugin 2.0.18. Separate from the mailbox plugin: this
// reports UI state only and never admits, reads, or acknowledges mailbox work.
import { spawn } from 'node:child_process'
import { setTimeout as delay } from 'node:timers/promises'

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
    const reconciliations = new Map()
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
      for (const sessionID of reconciliations.keys()) cancelReconciliation(sessionID)
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
      if (kind === 'permission') pruneReconciliation(session)
      return true
    }

    function cancelReconciliation(sessionID) {
      const work = reconciliations.get(sessionID)
      if (!work) return
      reconciliations.delete(sessionID)
      work.controller.abort()
    }

    function pruneReconciliation(sessionID) {
      const work = reconciliations.get(sessionID)
      if (!work) return
      for (const [id, request] of work.requests) {
        if (pending.get(id) !== request) work.requests.delete(id)
      }
      if (!work.requests.size) cancelReconciliation(sessionID)
    }

    function reconcilePermissions(sessionID) {
      if (lifetime.signal.aborted) return
      pruneReconciliation(sessionID)
      const requests = [...pending].filter(([, request]) => request.kind === 'permission' && request.session_id === sessionID)
      if (!requests.length) return
      const current = reconciliations.get(sessionID)
      if (current) {
        for (const [id, request] of requests) current.requests.set(id, request)
        return
      }
      const work = { requests: new Map(requests), controller: new AbortController() }
      reconciliations.set(sessionID, work)
      const signal = AbortSignal.any([lifetime.signal, work.controller.signal])
      void (async () => {
        while (!signal.aborted) {
          const snapshot = [...work.requests]
          try {
            // Released 2.0.18 exposes permission.list, but no session.form.list.
            // Interrupted permission cleanup can omit permission.replied.
            const querySignal = AbortSignal.any([signal, AbortSignal.timeout(4000)])
            const live = await ctx.permission.list({ sessionID }, { signal: querySignal })
            if (signal.aborted) return
            querySignal.throwIfAborted()
            if (!Array.isArray(live)) throw new Error('Invalid OpenCode permission list response')
            const ids = new Set()
            for (const request of live) {
              if (!request || request.sessionID !== sessionID || typeof request.id !== 'string' || !request.id) {
                throw new Error('Invalid OpenCode permission list response')
              }
              ids.add(key(request.sessionID, 'permission', request.id))
            }
            let changed = false
            for (const [id, request] of snapshot) {
              // An older read cannot remove an identity reopened while it was in flight.
              if (!ids.has(id) && pending.get(id) === request) {
                pending.delete(id)
                changed = true
              }
              if (work.requests.get(id) === request) work.requests.delete(id)
            }
            if (changed) report()
          } catch (error) {
            if (signal.aborted) return
            warn(error)
          }
          pruneReconciliation(sessionID)
          if (signal.aborted) return
          // Only failed or newly queued scopes remain; successful live requests
          // are not polled. One query/delay per session keeps recovery bounded.
          await delay(1000, undefined, { signal, ref: false })
        }
      })().catch(error => { if (!signal.aborted) warn(error) })
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
            reconcilePermissions(data.sessionID)
            report()
            break
          case 'session.deleted':
            cancelReconciliation(data.sessionID)
            busy.delete(data.sessionID)
            for (const [id, request] of pending) if (request.session_id === data.sessionID) pending.delete(id)
            report()
            break
          case 'permission.asked':
            if (open(data.sessionID, 'permission', data.id)) report()
            break
          case 'permission.replied':
            if (!pending.delete(key(data.sessionID, 'permission', data.requestID))) break
            reconcilePermissions(data.sessionID)
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
