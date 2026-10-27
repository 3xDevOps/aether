import { useState } from 'react'
import { EyeOff } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { message } from '@/lib/format'
import { useStore } from '@/store'

export const temporaryBanner = "Temporary: the assistant won't read or save your preferences until New conversation."

export function TemporaryChatToggle({ temporary }: { temporary: boolean }) {
  const [busy, setBusy] = useState(false)
  return (
    <Button
      variant={temporary ? 'secondary' : 'ghost'}
      size="icon-sm"
      label="Temporary chat"
      aria-pressed={temporary}
      disabled={busy}
      onClick={() => {
        const identity = useStore.getState().identityKey
        useStore.getState().setAssistantError(null)
        setBusy(true)
        api.assistantSettings({ temporary: !temporary }).then(
          (settings) => {
            const state = useStore.getState()
            if (state.identityKey === identity && state.assistantStatus) state.setAssistantStatus({ ...state.assistantStatus, ...settings })
          },
          (err: unknown) => {
            if (useStore.getState().identityKey === identity) useStore.getState().setAssistantError(message(err))
          },
        ).finally(() => setBusy(false))
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
