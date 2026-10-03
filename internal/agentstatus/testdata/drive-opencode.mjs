// Drive the real native plugin and reporter subprocess, exposing callback
// boundaries for the scheduler's deterministic activity-clock regression.
import { spawn } from 'node:child_process'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'

const version = process.argv[2]
const logPath = process.argv[3]
const flush = () => new Promise(resolve => setImmediate(resolve))
const children = new Set()
const sandbox = vm.createContext({ process, AbortController, AbortSignal, console })
const module = new vm.SourceTextModule(readFileSync(new URL('status.js', import.meta.url), 'utf8'), { context: sandbox })
await module.link(specifier => {
  if (specifier !== 'node:child_process') throw new Error(`unexpected import: ${specifier}`)
  return new vm.SyntheticModule(['spawn'], function () {
    this.setExport('spawn', (...args) => {
      const child = spawn(...args)
      const closed = new Promise(resolve => child.once('close', resolve))
      children.add(closed)
      void closed.then(() => children.delete(closed))
      return child
    })
  }, { context: sandbox })
})
await module.evaluate()

let emit
let dispose = () => {}
if (version === 'v1') {
  const hooks = await module.namespace.AetherStatus({
    client: { app: { log: async ({ body }) => { throw new Error(body.message) } } },
  })
  emit = (type, properties) => hooks.event({ event: { type, properties } })
} else {
  const events = []
  let waiter
  dispose = module.namespace.default.setup({
    location: { directory: '/workspace' },
    permission: { list: async () => [] },
    event: { async *subscribe({ signal }) {
      while (!signal.aborted) {
        if (events.length) { yield events.shift(); continue }
        waiter = Promise.withResolvers()
        const resume = waiter.resolve
        signal.addEventListener('abort', resume, { once: true })
        await waiter.promise
        signal.removeEventListener('abort', resume)
      }
    } },
  })
  emit = async (type, data) => {
    events.push({ type, data, location: { directory: '/workspace' } })
    waiter?.resolve()
  }
}

const checkpoints = []
let consumed = 0
async function checkpoint(phase) {
  await flush()
  while (children.size) { await Promise.all(children); await flush() }
  const lines = readFileSync(logPath, 'utf8').trim().split('\n')
  checkpoints.push({ phase, reports: lines.slice(consumed).map(line => JSON.parse(line)) })
  consumed = lines.length
}
const start = sessionID => emit(version === 'v1' ? 'session.status' : 'session.execution.started',
  { sessionID, ...(version === 'v1' ? { status: { type: 'busy' } } : {}) })
const end = sessionID => emit(version === 'v1' ? 'session.idle' : 'session.execution.succeeded', { sessionID })

await start('root')
await checkpoint('start')
for (let i = 0; i < 4; i++) {
  // V1 emits ongoing busy status; V2 emits starts for concurrent sessions.
  await start(version === 'v1' ? 'root' : `child-${i}`)
  await checkpoint('work')
}
if (version === 'v2') {
  await end('root')
  await checkpoint('background')
  for (let i = 0; i < 4; i++) await end(`child-${i}`)
} else {
  await end('root')
}
await checkpoint('idle')
dispose()
console.log(JSON.stringify(checkpoints))
