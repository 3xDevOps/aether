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

// A child agent spawned by the run's agent inherits the run's environment,
// including the marker naming the process that reports. Set it to someone
// else and the extension must register nothing at all.
if (scenario === 'foreign-owner') process.env.AETHER_STATUS_OWNER = '1'

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
// Busy on the first two checks, idle on the third: the agent kept working
// past agent_end and then went quiet, with no agent_settled to say so.
let checks = 0
const busyThenIdle = { isIdle: () => ++checks > 2 }

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
  case 'idle-later':
    // Legacy pi and omp are idle by the time agent_end fires; a modern pi
    // can still be retrying or compacting. The extension has to keep
    // asking until the answer changes, and report the end exactly once.
    fire('agent_end', {}, busyThenIdle)
    break
  case 'foreign-owner':
    fire('agent_start')
    fire('message_end')
    fire('agent_end', {}, idle)
    break
  case 'will-continue':
    // omp is already idle when it says more work follows, so nothing but
    // the willContinue flag is holding the report back.
    fire('agent_end', { willContinue: true }, idle)
    break
  case 'unreachable':
    // The reporter runs and exits 0 but never reaches the server, saying so
    // on stderr once per report. Two reports, so the warning the extension
    // forwards has two chances to repeat itself.
    fire('agent_start')
    fire('agent_end', {}, idle)
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
