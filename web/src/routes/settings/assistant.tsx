import { Fragment, useEffect, useState } from 'react'
import { useAssistantAvailable } from '@/components/assistant/button'
import { AssistantSignIn } from '@/components/assistant/sign-in'
import { Confirm } from '@/components/confirm'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import type { Api } from '@/lib/api'
import type { AssistantSettings } from '@/lib/assistant-types'
import { errorSentence } from '@/lib/format'
import { SettingRow, SettingsSection, TryAgain } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { refreshAssistant } from '@/store/assistant-stream'

export const autoApproveLabel = 'Let the assistant act without asking'

export const autoApproveWarning =
  'The assistant will launch runs, message and stop your runs and save your environment on its own, with no approval card. Text it reads from runs, files or web pages could steer it into doing so.'

const scope: [string, string][] = [
  ['Without a card', 'Launching runs, messaging and stopping your own runs, saving your environment. Each is noted in the assistant’s conversation before it runs.'],
  ['Always asks', 'Messaging or stopping another member’s run.'],
  ['Not affected', 'The assistant’s own shell and file tools, which run without asking either way.'],
]

export function AssistantSection({ client }: { client: Api }) {
  const available = useAssistantAvailable()
  const [loaded, setLoaded] = useState<boolean | null>(null)
  const stored = useStore((s) => s.assistantStatus?.auto_approve)
  const on = loaded === null ? null : (stored ?? loaded)
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    if (!available) return
    let live = true
    client.assistantSettings().then(
      (s) => live && setLoaded(s.auto_approve),
      (err: unknown) => live && setError(errorSentence(err)),
    )
    return () => {
      live = false
    }
  }, [client, available, attempt])

  if (!available) return null

  const show = (s: AssistantSettings) => {
    setLoaded(s.auto_approve)
    const status = useStore.getState().assistantStatus
    if (status) useStore.getState().setAssistantStatus({ ...status, ...s })
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

  const help = on === null
    ? error ? null : 'Reading the setting…'
    : on
      ? 'On. The assistant launches runs, messages and stops your runs and saves your environment without a card.'
      : 'Off. The assistant asks on a card before it launches, messages or stops a run, or saves your environment.'

  return (
    <>
    <SignInSection client={client} />
    <SettingsSection title="Approvals" id="auto-approve" scope="Your account">
      <SettingRow
        label={autoApproveLabel}
        labelFor="settings-assistant-auto-approve"
        help={help}
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
        <dl aria-label="What auto-approval covers" className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm [&_dt]:text-muted">
          {scope.map(([term, text]) => (
            <Fragment key={term}>
              <dt>{term}</dt>
              <dd className="min-w-0">{text}</dd>
            </Fragment>
          ))}
        </dl>
        {error && <Callout tone="failed" role="alert">{error}</Callout>}
        {error && on === null && (
          <TryAgain onClick={() => { setError(null); setAttempt((n) => n + 1) }} />
        )}
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
    </>
  )
}

/** Signs an agent in again for the assistant, as when its sign-in expired: a sign-in that is still there counts as ready. */
function SignInSection({ client }: { client: Api }) {
  const agents = useStore((s) => s.assistantStatus?.agents)
  const [signing, setSigning] = useState('')
  if (!agents?.length) return null
  return (
    <SettingsSection title="Sign-in" id="assistant-sign-in" scope="Your account">
      {agents.map((agent) => (
        <SettingRow
          key={agent.name}
          label={agent.display_name}
          help={agent.ready ? 'Signed in for the assistant.' : agent.reason}
          control={signing !== agent.name && (
            <Button size="sm" variant="secondary" onClick={() => setSigning(agent.name)}>{agent.ready ? 'Sign in again' : 'Sign in'}</Button>
          )}
        >
          {signing === agent.name && (
            <AssistantSignIn
              agent={agent}
              client={client}
              onDone={() => {
                setSigning('')
                void refreshAssistant(useStore, client)
              }}
            />
          )}
        </SettingRow>
      ))}
    </SettingsSection>
  )
}
