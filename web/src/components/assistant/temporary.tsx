import { EyeOff } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'

export const temporaryBanner = "Temporary: the assistant won't use or save your preferences, and this chat is discarded when you start a new one."

export function TemporaryChatToggle({ temporary }: { temporary: boolean }) {
  return (
    <Button
      variant={temporary ? 'secondary' : 'ghost'}
      size="icon-sm"
      label="Temporary chat"
      aria-pressed={temporary}
      onClick={() => {
        const state = useStore.getState()
        state.setAssistantError(null)
        api.assistantSettings({ temporary: !temporary }).then(
          (settings) => {
            const status = useStore.getState().assistantStatus
            if (status) useStore.getState().setAssistantStatus({ ...status, ...settings })
          },
          (err: unknown) => useStore.getState().setAssistantError(message(err)),
        )
      }}
    >
      <EyeOff />
    </Button>
  )
}

export function TemporaryBanner() {
  const temporary = useStore((s) => s.assistantStatus?.temporary === true)
  return temporary ? <p role="status" className="text-ui-sm text-muted">{temporaryBanner}</p> : null
}
