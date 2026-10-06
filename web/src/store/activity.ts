interface ToolCall {
  tenses: [string, string]
  target: string
}

/** What a run's agent is doing, from its latest `run.agent` event. */
export interface RunActivity {
  /** "Reading" while a tool runs, "Read" once it returned. */
  verb: string
  /** The file, command or task the tool was given, else the tool's name. */
  target: string
  at: string
  /** Calls still running, by `tool_use_id`; parallel calls return in any order. */
  running?: Record<string, ToolCall>
}

export interface AgentPayload {
  kind?: string
  tool?: string
  /** Set by an Enhanced run, whose `tool` is an ACP tool kind such as `execute`. */
  verb?: string
  tool_use_id?: string
  detail?: string
  is_error?: boolean
}

/** Present and past tense per tool, keyed by lower-cased tool name. */
const toolVerbs: Record<string, [string, string]> = {
  read: ['Reading', 'Read'],
  edit: ['Editing', 'Edited'],
  multiedit: ['Editing', 'Edited'],
  notebookedit: ['Editing', 'Edited'],
  write: ['Writing', 'Wrote'],
  bash: ['Running', 'Ran'],
  grep: ['Searching', 'Searched'],
  glob: ['Searching', 'Searched'],
  websearch: ['Searching', 'Searched'],
  webfetch: ['Fetching', 'Fetched'],
  todowrite: ['Planning', 'Planned'],
}

const delegating: [string, string] = ['Delegating', 'Delegated']

/** The activity after one agent event, or `previous` when it changes nothing shown. */
export function nextActivity(
  previous: RunActivity | undefined,
  payload: AgentPayload,
  at: string,
): RunActivity | undefined {
  switch (payload.kind) {
    case 'tool_call':
    case 'subagent': {
      const tool = payload.tool ?? ''
      const call: ToolCall = payload.kind === 'subagent'
        ? { tenses: delegating, target: payload.detail || tool }
        : payload.verb
          ? { tenses: [payload.verb, payload.verb], target: payload.detail ?? '' }
          : {
              tenses: toolVerbs[tool.toLowerCase()] ?? [`Using ${tool || 'a tool'}`, `Used ${tool || 'a tool'}`],
              target: payload.detail || tool,
            }
      const running = payload.tool_use_id
        ? { ...previous?.running, [payload.tool_use_id]: call }
        : previous?.running
      return { verb: call.tenses[0], target: call.target, at, running }
    }
    case 'tool_result': {
      if (!previous) return previous
      const id = payload.tool_use_id
      const ended = id ? previous.running?.[id] : undefined
      if (ended) {
        const rest = Object.entries(previous.running ?? {}).filter(([key]) => key !== id)
        const running = rest.length ? Object.fromEntries(rest) : undefined
        const latest = rest.at(-1)?.[1]
        if (latest) return { verb: latest.tenses[0], target: latest.target, at, running }
        return { verb: payload.is_error ? 'Failed' : ended.tenses[1], target: ended.target, at, running }
      }
      if (payload.is_error) return { ...previous, verb: 'Failed', at }
      const tenses = [...Object.values(toolVerbs), delegating].find(([present]) => present === previous.verb)
      const verb = tenses?.[1] ?? previous.verb.replace(/^Using /, 'Used ')
      return { ...previous, verb, at }
    }
    default:
      return previous
  }
}
