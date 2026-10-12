export function modelHints(harness: string): { placeholder: string; suggestions: string[] } {
  switch (harness) {
    case 'claude':
      return { placeholder: 'opus, sonnet, haiku or a full model id', suggestions: ['opus', 'sonnet', 'haiku'] }
    case 'codex':
      return { placeholder: 'a model id such as gpt-5-codex', suggestions: [] }
    case 'opencode':
      return { placeholder: 'provider/model, such as anthropic/claude-sonnet-4-5', suggestions: [] }
    case 'pi':
      return { placeholder: 'provider/model, optionally :<thinking>', suggestions: [] }
    case 'omp':
      return { placeholder: 'opus, gpt-5.2 or openai/gpt-5.2', suggestions: [] }
    default:
      return { placeholder: "the agent's model id", suggestions: [] }
  }
}
