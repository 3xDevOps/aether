// Drives the staged status extension through one scenario, standing in for
// pi: it hands the extension a pi object that records handlers, fires
// events at them the way the harness does, and waits for the report chain
// to drain before exiting.
const scenario = process.argv[2]
const handlers: Record<string, Array<(event?: any, ctx?: any) => void>> = {}
const pi = {
  on(event: string, handler: (event?: any, ctx?: any) => void) {
    ;(handlers[event] ||= []).push(handler)
  },
}

const copies = scenario === 'double' ? ['./status.ts', './status-copy.ts'] : ['./status.ts']
for (const copy of copies) {
  const load = await import(copy)
  load.default(pi)
}

function fire(event: string, payload?: any, ctx?: any): void {
  for (const handler of handlers[event] || []) handler(payload, ctx)
}

const idle = { isIdle: () => true }
const busy = { isIdle: () => false }

switch (scenario) {
  case 'turn':
    fire('agent_start')
    fire('tool_call', { toolName: 'bash' })
    fire('tool_execution_start', { toolName: 'bash' })
    fire('tool_execution_end', { toolName: 'bash' })
    fire('message_end')
    fire('agent_end', {}, idle)
    break
  case 'late-message':
    // Legacy pi and omp have no ctx.isIdle, so the turn ends on the spot -
    // and pi finalizes the assistant's last message just after it.
    fire('agent_end', {}, {})
    fire('message_end')
    break
  case 'settled':
    // The agent is still busy at agent_end, so the extension waits; the
    // harness then says the turn really is over.
    fire('agent_end', {}, busy)
    await new Promise((done) => setTimeout(done, 100))
    fire('agent_settled')
    break
  case 'will-continue':
    // omp is already idle when it says more work follows, so nothing but
    // the willContinue flag is holding the report back.
    fire('agent_end', { willContinue: true }, idle)
    break
  case 'double':
    fire('agent_start')
    fire('message_end')
    fire('agent_end', {}, idle)
    break
  default:
    throw new Error('unknown scenario ' + scenario)
}

// Let the extension's own timers run, then drain the chain it posts
// reports through - the same object every copy of the file shares.
await new Promise((done) => setTimeout(done, 200))
await globalThis['__aetherStatusReports'].posts
