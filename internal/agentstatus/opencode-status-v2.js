// OpenCode @opencode/plugin 2.0.18. Separate from the mailbox plugin: this
// reports UI state only and never admits, reads, or acknowledges mailbox work.
import { spawn } from 'node:child_process'

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
    let last
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
      // A child starting work must not clear another session's permission or
      // question. These canonical events reuse FromOpenCodeEvent's wire map.
      const permission = [...pending.values()].some(request => request.type === 'permission')
      const event = permission ? 'permission.asked' : pending.size ? 'question.asked' : busy.size ? 'session.status' : 'session.idle'
      if (event === last) return
      last = event
      const args = ['report', 'opencode', '--event', event]
      if (event === 'session.status') args.push('--status', 'busy')
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

    async function reconcilePermissions(sessionID) {
      const requests = [...pending].filter(([, request]) => request.type === 'permission' && request.sessionID === sessionID)
      if (!requests.length) return
      // Permission.assert removes interrupted requests during cleanup without
      // publishing permission.replied. Terminal execution events follow cleanup.
      const live = await ctx.permission.list({ sessionID }, { signal: lifetime.signal })
      if (lifetime.signal.aborted) return
      const ids = new Set(live.map(request => `permission:${request.id}`))
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
            busy.add(data.sessionID)
            report()
            break
          case 'session.execution.succeeded':
          case 'session.execution.failed':
          case 'session.execution.interrupted':
            busy.delete(data.sessionID)
            await reconcilePermissions(data.sessionID).catch(warn)
            if (lifetime.signal.aborted) return
            report()
            break
          case 'session.deleted':
            busy.delete(data.sessionID)
            for (const [id, request] of pending) if (request.sessionID === data.sessionID) pending.delete(id)
            report()
            break
          case 'permission.asked':
            pending.set(`permission:${data.id}`, { sessionID: data.sessionID, type: 'permission' })
            report()
            break
          case 'permission.replied':
            if (pending.delete(`permission:${data.requestID}`)) report()
            break
          case 'form.created':
            pending.set(`form:${data.form.id}`, { sessionID: data.form.sessionID, type: 'form' })
            report()
            break
          case 'form.replied':
          case 'form.cancelled':
            if (pending.delete(`form:${data.id}`)) report()
            break
        }
      }
      if (!lifetime.signal.aborted) warn('OpenCode lifecycle stream ended')
    })().catch(warn)
    return dispose
  },
}
