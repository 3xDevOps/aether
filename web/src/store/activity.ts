/** What a run's agent is doing, from its latest `run.agent` event. */
export interface RunActivity {
  /** "Reading" while a tool runs, "Read" once it returned. */
  verb: string
  /** The file, command or task the tool was given, else the tool's name. */
  target: string
  at: string
}

/** The `run.agent` payload fields the activity line reads. */
export interface AgentPayload {
  kind?: string
  tool?: string
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

/**
 * The activity after one agent event, or `previous` when the event says
 * nothing a state line shows. A tool result carries no tool name, so it
 * puts the call it ends into the past tense.
 */
export function nextActivity(
  previous: RunActivity | undefined,
  payload: AgentPayload,
  at: string,
): RunActivity | undefined {
  switch (payload.kind) {
    case 'tool_call': {
      const tool = payload.tool ?? ''
      const [verb] = toolVerbs[tool.toLowerCase()] ?? [`Using ${tool || 'a tool'}`]
      return { verb, target: payload.detail || tool, at }
    }
    case 'subagent':
      return { verb: delegating[0], target: payload.detail || payload.tool || '', at }
    case 'tool_result': {
      if (!previous) return previous
      if (payload.is_error) return { ...previous, verb: 'Failed', at }
      const tenses = [...Object.values(toolVerbs), delegating].find(([present]) => present === previous.verb)
      const verb = tenses?.[1] ?? previous.verb.replace(/^Using /, 'Used ')
      return { ...previous, verb, at }
    }
    default:
      return previous
  }
}
