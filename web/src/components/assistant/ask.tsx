import { useAssistantAvailable } from '@/components/assistant/button'
import { Button, type ButtonProps } from '@/components/ui/button'
import { useStore } from '@/store'
import type { AssistantAsk } from '@/store/assistant'

/** Opens the assistant with `ask`'s target attached and its draft to edit; never sends. Absent for a member who cannot use the assistant. */
export function AskAssistantButton({ ask, ...props }: ButtonProps & { ask: AssistantAsk }) {
  const available = useAssistantAvailable()
  if (!available) return null
  return <Button {...props} onClick={() => useStore.getState().askAssistant(ask)} />
}
