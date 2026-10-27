// Mirrors needsInputReasonPrefix in internal/scheduler/reported.go.
export const needsInputPrefix = 'needs input: '
export const stalledPrefix = 'stalled:'
// Mirrors stalledReason in internal/scheduler/liveness.go.
export const possibleStallPrefix = 'stalled: possibly — '

// Mirrors the reason strings internal/scheduler, internal/agentstatus and internal/coord write.
const stopReasons: Record<string, string> = {
  max_tokens: 'the reply reached its token limit',
  max_turn_requests: 'too many model requests in one turn',
  refusal: 'the agent refused',
}

const stoppedBecause = (stop: string) => `Stopped: ${stopReasons[stop] ?? stop}`

const turnEnds: Record<string, string> = {
  end_turn: 'The agent finished its turn',
  cancelled: 'The turn was cancelled',
}

// Phases as stalledReason writes them, dashes spaced: domain.TurnPhase in internal/domain/liveness.go.
const stallPhases: Record<string, string> = {
  setup: 'while the run was starting',
  'awaiting first event': "while awaiting the model's first reply",
  streaming: 'since the agent last replied',
  'tool running': 'while a tool was running',
  'awaiting input': 'while a request waited for an answer',
  'between turns': 'between turns',
}

/** A Go duration with its zero tail dropped: 6m0s reads 6m, 1h0m0s reads 1h. */
const goSpan = (span: string) => span.replace(/([hm])0s$/, '$1').replace(/h0m$/, 'h')

const savedResults = "the worker's saved results"

const plainReasons: [RegExp, (...groups: string[]) => string][] = [
  [/^(.*); retained container$/, (rest) => plainReason(rest)],
  [/^agent reported success$/, () => 'Agent reported success'],
  [/^agent reported failure$/, () => 'Agent reported failure'],
  [/^agent exited; results committed$/, () => 'Agent exited and its changes were committed'],
  [/^enhanced session failed: (.*)$/s, (why) => `Enhanced could not start: ${why}`],
  [/^enhanced session ended: (.*)$/s, (why) => `Agent disconnected: ${why}`],
  [/^enhanced turn failed: (.*)$/s, (why) => `Turn failed: ${why}`],
  [/^agent exited (-?\d+)(.*)$/s, (code, rest) => `Agent exited with code ${code}${rest}`],
  [/^worker finished$/, () => 'Worker finished'],
  [/^closed$/, () => 'Closed'],
  [/^killed$/, () => 'Stopped'],
  [/^retained container expired$/, () => 'Its container was removed when it expired'],
  [/^retained container reclaimed for disk space$/, () => 'Its container was removed early because disk space was low'],
  [/^retained container unavailable$/, () => 'Its container is no longer available'],
  [/^server restarted: container exited (-?\d+)$/, (code) => `Its container had stopped with exit code ${code} when the server restarted`],
  [/^(?:agent idle|turn ended without a report)$/, () => 'Turn ended without a report'],
  [/^no update for ([^\s:]+)(: .*)?$/s, (span, summary = '') => `No update for ${span}${summary}`],
  [/^waiting for (\d+ workers?)$/, (workers) => `Waiting for ${workers}`],
  [/^waiting for (\d+ child runs?)$/, (children) => `Waiting for ${children}`],
  [/^agent resumed$/, () => 'Agent resumed'],
  [/^agent failed$/, () => 'Agent failed'],
  [/^stalled: no output or file changes for (.+)$/, (span) => `No output or file changes for ${span}`],
  [/^stalled: possibly — no progress for (\S+), (.+), transport (\w+)$/, (span, phase, transport) => `No progress for ${goSpan(span)} ${stallPhases[phase] ?? phase}; connection ${transport}`],
  [/^blocked: (.*)$/s, (why) => `Blocked: ${why}`],
  [/^needs input: (.*)$/s, (question) => `Agent asks: ${question}`],
  [/^working in background(: \d+ tasks?)?$/, (count = '') => `Working in background${count}`],
  [/^turn cancelled$/, () => 'Turn cancelled'],
  [/^turn interrupted$/, () => 'Turn interrupted'],
  [/^turn ended: (\w+)$/, (stop) => (stop in stopReasons ? stoppedBecause(stop) : turnEnds[stop] ?? `The agent's turn ended (${stop})`)],
  [/^the agent's turn ended: (\w+)$/, (stop) => (stop in stopReasons ? stoppedBecause(stop) : turnEnds[stop] ?? `The agent's turn ended (${stop})`)],
  [/^send the task to the agent: (.*)$/s, (err) => `Could not send the task to the agent: ${err}`],
  [/^review retained evidence$/, () => `Review ${savedResults}`],
  [/^resolve the blocker using the retained evidence$/, () => `Resolve the blocker using ${savedResults}`],
  [/^retained evidence (?:is )?unavailable(.*)$/s, (rest) => `The worker's saved results are unavailable${rest}`],
]

export function plainReason(reason: string): string {
  for (const [pattern, words] of plainReasons) {
    const match = pattern.exec(reason)
    if (match) return words(...match.slice(1))
  }
  return reason
}

