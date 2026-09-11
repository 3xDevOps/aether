// Aether's agent status reporter for pi and oh-my-pi (omp).
//
// The server writes this file into the run's coordination directory and
// launches the harness with "-e <file>". Each lifecycle event below is
// handed to the staged server binary mounted beside it, which maps it onto
// "working" or "waiting" and reports it on the run's own socket
// (internal/agentstatus, "aether-server report pi").
//
// Nothing is imported: pi and omp expose the same extension API but
// publish its types under different package names, so the one file both
// load has to name neither.

// REPORTER is the staged server binary inside the run container
// (internal/mcpbridge.BinaryPath).
const REPORTER = '/opt/aether/aether-server'

// OWNER marks the process that reports. A child agent inherits its
// parent's environment, and a member who also keeps this file in
// ~/.pi/agent/extensions loads it twice, so the marker leaves exactly one
// reporter per process.
const OWNER = 'AETHER_STATUS_OWNER'

// Re-checking idleness after a turn ends: modern pi can retry, compact or
// follow up past agent_end, so the report waits for the agent to actually
// go quiet. Legacy pi and omp are idle by then and answer on the first
// check.
const IDLE_RECHECK_MS = 25
const IDLE_RECHECK_MAX_MS = 250

// pi awaits extension handlers, so no handler here awaits a report: the
// agent must never wait on a socket to keep working. The chain is what
// keeps the reports in order all the same - a "working" that overtook a
// "waiting" would leave the run reading Working with nothing left to
// correct it.
let posts: Promise<void> = Promise.resolve()
let warned = false

function warnOnce(err: unknown): void {
  if (warned) return
  warned = true
  console.warn('[aether] status report failed:', err)
}

function spawnReport(args: string[]): Promise<void> {
  return new Promise((resolve) => {
    try {
      const { spawn } = require('child_process')
      const child = spawn(REPORTER, args, { stdio: 'ignore' })
      child.on('error', (err: unknown) => {
        warnOnce(err)
        resolve()
      })
      child.on('close', () => resolve())
    } catch (err) {
      warnOnce(err)
      resolve()
    }
  })
}

function report(event: string, tool?: unknown): void {
  const args = ['report', 'pi', '--event', event]
  if (typeof tool === 'string' && tool) args.push('--tool', tool)
  posts = posts.then(() => spawnReport(args)).catch(() => {})
}

export default function (pi): void {
  const self = String(process.pid)
  const owner = process.env[OWNER]
  if (owner && owner !== self) return
  process.env[OWNER] = self

  // ended is what the agent last said about this turn. It exists for
  // message_end alone: pi finalizes the assistant's last message around the
  // end of the turn, and a "working" landing after "waiting" would strand
  // the run at Working until the stall threshold.
  let ended = false
  let settledSupported = false
  let recheck: ReturnType<typeof setTimeout> | null = null
  let recheckDelay = IDLE_RECHECK_MS

  function clearRecheck(): void {
    if (recheck !== null) clearTimeout(recheck)
    recheck = null
  }

  function startTurn(event: string): void {
    clearRecheck()
    ended = false
    report(event)
  }

  function endTurn(event: string): void {
    if (ended) return
    ended = true
    clearRecheck()
    report(event)
  }

  function recheckIdle(ctx, delay: number): void {
    recheck = setTimeout(() => {
      recheck = null
      if (settledSupported || ended) return
      let idle = false
      try {
        idle = ctx.isIdle()
      } catch (err) {
        warnOnce(err)
        return
      }
      if (idle) {
        endTurn('agent_end')
        return
      }
      recheckIdle(ctx, recheckDelay)
      recheckDelay = Math.min(recheckDelay * 2, IDLE_RECHECK_MAX_MS)
    }, delay)
    if (typeof recheck.unref === 'function') recheck.unref()
  }

  pi.on('before_agent_start', () => startTurn('before_agent_start'))
  pi.on('agent_start', () => startTurn('agent_start'))
  pi.on('tool_call', (event) => report('tool_call', event?.toolName))
  pi.on('tool_execution_start', (event) => report('tool_execution_start', event?.toolName))
  pi.on('tool_execution_end', (event) => report('tool_execution_end', event?.toolName))
  pi.on('tool_approval_requested', (event) => report('tool_approval_requested', event?.toolName))
  pi.on('tool_approval_resolved', (event) => report('tool_approval_resolved', event?.toolName))
  pi.on('message_end', () => {
    if (ended) return
    report('message_end')
  })

  // Where the harness has agent_settled, that is the end of the turn and
  // agent_end is only its first half.
  pi.on('agent_settled', () => {
    settledSupported = true
    endTurn('agent_settled')
  })

  pi.on('agent_end', (event, ctx) => {
    if (settledSupported) return
    // omp says outright when more work follows this end.
    if (event?.willContinue) return
    if (!ctx || typeof ctx.isIdle !== 'function') {
      endTurn('agent_end')
      return
    }
    clearRecheck()
    recheckDelay = IDLE_RECHECK_MS
    recheckIdle(ctx, 0)
  })
}
