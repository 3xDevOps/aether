import type { RunInputRequest } from '@/lib/types'

export const inputTitle: Record<RunInputRequest['kind'], string> = {
  permission: 'The agent asks for permission',
  question: 'The agent asks a question',
  form: 'The agent asks you to fill in a form',
  extension_ui: 'The agent opened a dialog',
}

export function inputHint(hasAgentTerminal: boolean): string {
  return hasAgentTerminal ? 'Answer in the terminal.' : 'Answer it from the agent’s session.'
}
