import { useEffect, useState } from 'react'
import { useAssistantAvailable } from '@/components/assistant/button'
import { Confirm } from '@/components/confirm'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import type { Api } from '@/lib/api'
import type { AssistantSettings } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'

export const autoApproveLabel = 'Let the assistant act without asking'

export const autoApproveWarning =
  'The assistant will launch, message and stop runs and save your environment on its own, with no approval card. Text it reads from runs, files or web pages could steer it into doing so.'

export function AssistantSection({ client }: { client: Api }) {
  const available = useAssistantAvailable()
  const [on, setOn] = useState<boolean | null>(null)
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!available) return
    let live = true
    client.assistantSettings().then(
      (s) => live && setOn(s.auto_approve),
      (err: unknown) => live && setError(errorSentence(err)),
    )
    return () => {
      live = false
    }
  }, [client, available])

  if (!available) return null

  const show = (s: AssistantSettings) => {
    setOn(s.auto_approve)
    const status = useStore.getState().assistantStatus
    if (status) useStore.getState().setAssistantStatus({ ...status, auto_approve: s.auto_approve })
  }
  // A refused change may still have been stored, so the row shows what the server holds.
  const save = (next: boolean) => client.assistantSettings({ auto_approve: next }).then(show, async (err: unknown) => {
    await client.assistantSettings().then(show, () => {})
    throw err
  })
  const turnOff = () => {
    setBusy(true)
    setError(null)
    save(false).catch((err: unknown) => setError(errorSentence(err))).finally(() => setBusy(false))
  }

  return (
    <SettingsSection title="Assistant">
      <SettingRow
        label={autoApproveLabel}
        labelFor="settings-assistant-auto-approve"
        help={on
          ? 'On. Launching, messaging and stopping runs and saving your environment happen without a card. Each one is noted in the assistant’s conversation.'
          : 'Off. The assistant asks on a card before it launches, messages or stops a run, or saves your environment.'}
        control={(
          <Checkbox
            id="settings-assistant-auto-approve"
            checked={on === true}
            disabled={on === null || busy}
            onCheckedChange={(checked) => {
              setError(null)
              if (checked === true) setConfirming(true)
              else turnOff()
            }}
          />
        )}
      >
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
      </SettingRow>
      {confirming && (
        <Confirm
          title="Let the assistant act without asking?"
          description={autoApproveWarning}
          action="Turn on"
          pending="Turning on…"
          onConfirm={() => save(true)}
          onDone={() => {}}
          onClose={() => setConfirming(false)}
        />
      )}
    </SettingsSection>
  )
}
