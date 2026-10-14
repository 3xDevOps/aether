import { Sparkles } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { shortcutLabel } from '@/lib/keybindings'
import { useStore } from '@/store'
import { assistantWaiting } from '@/store/assistant'
import { canLaunch } from '@/lib/commands'
import { useCapability, useSelfRole } from '@/store/hooks'

export const assistantTriggerID = 'assistant-trigger'

function label(waiting: number): string {
  return waiting > 0 ? `Assistant, ${waiting} ${waiting === 1 ? 'request' : 'requests'} waiting` : 'Assistant'
}

/** assistant.prompt needs the Launch capability, so anyone who cannot start a run is not offered the assistant. */
export function useAssistantAvailable(): boolean {
  const cap = useCapability()
  const role = useSelfRole()
  return cap.hasMethod('assistant.status') && canLaunch({ cap, role })
}

/** Mounted in exactly one place at a time: the phone toolbar, the sidebar header, or the top bar when the sidebar is hidden. */
export function AssistantButton({ variant = 'ghost' }: { variant?: 'ghost' | 'secondary' }) {
  const supported = useAssistantAvailable()
  const open = useStore((s) => s.assistantOpen)
  const setOpen = useStore((s) => s.setAssistantOpen)
  const waiting = useStore(assistantWaiting)
  if (!supported) return null
  return (
    <span className="relative shrink-0">
      <Button
        id={assistantTriggerID}
        variant={variant}
        size="icon"
        label={label(waiting)}
        hint={`Assistant · ${shortcutLabel('assistant')}`}
        aria-expanded={open}
        aria-controls="assistant"
        onClick={() => setOpen(!open)}
      >
        <Sparkles />
      </Button>
      {waiting > 0 && <span aria-hidden className="pointer-events-none absolute top-2 right-2 size-2 rounded-full bg-state-needs-you" />}
    </span>
  )
}
