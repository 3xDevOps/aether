import { Fragment, type ReactNode, useEffect, useState } from 'react'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { ChildLimits, ChildSettingsParams } from '@/lib/types'
import { SettingRow, SettingsSection, TryAgain } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { saveChildSettings, useChildSettings, useChildSettingsRead } from '@/store/children'
import { useCapability } from '@/store/hooks'

export const childRunsLabel = 'Let my sessions start subsessions'

const scope: [string, string][] = [
  ['Allows', 'An agent in one of your sessions starts another agent as a subsession, on your account, then messages it and receives its result. You own every subsession.'],
  ['Limits', "The workspace budget and the workspace's subsession limits apply to every subsession."],
  ['One session', "Options → Subsessions in the launch dialog, or More → Allow subsessions on a session, overrides this for that session."],
]

export function limitsText(limits: ChildLimits): string {
  return `${limits.max_live} subsessions busy at once per parent session (more queue).`
}

/** The workspace's child-run limits as a sentence: read, reading, or the refusal. */
export function useChildLimitsText(client: Api, workspaceID: string, seq = 0): string {
  const [read, setRead] = useState<{ workspaceID: string; text: string } | null>(null)
  useEffect(() => {
    let live = true
    client.workspaceChildLimits(workspaceID).then(
      (limits) => live && setRead({ workspaceID, text: limitsText(limits) }),
      (err: unknown) => live && setRead({ workspaceID, text: errorSentence(err) }),
    )
    return () => {
      live = false
    }
  }, [client, workspaceID, seq])
  return read?.workspaceID === workspaceID ? read.text : 'Reading the limits…'
}

export function ChildLimitsRow({ client, workspaceID, seq, control }: { client: Api; workspaceID: string; seq: number; control: ReactNode }) {
  return <SettingRow label="Subsessions" help={useChildLimitsText(client, workspaceID, seq)} control={control} />
}

function WorkspaceLimits({ client }: { client: Api }) {
  const workspaceID = useStore((s) => s.activeWorkspace)
  const name = useStore((s) => s.workspaces[s.activeWorkspace]?.name)
  const text = useChildLimitsText(client, workspaceID)
  return <SettingRow id="child-limits" label={`Limits in ${name ?? workspaceID}`} help={`${text} An admin changes these in the workspace settings.`} />
}

export function ChildRunsSection({ client }: { client: Api }) {
  const cap = useCapability()
  const workspace = useStore((s) => s.activeWorkspace !== '')
  const [attempt, setAttempt] = useState(0)
  const settings = useChildSettingsRead(client, attempt)
  const readError = useChildSettings((s) => s.error)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!cap.hasMethod('child.settings')) return null

  // A refused change may still have been stored, so the rows show what the server holds.
  const change = (params: ChildSettingsParams) => {
    setBusy(true)
    setError(null)
    saveChildSettings(params, client).catch(async (err: unknown) => {
      await saveChildSettings({}, client).catch(() => {})
      throw err
    }).catch((err: unknown) => setError(errorSentence(err))).finally(() => setBusy(false))
  }
  const disabled = settings === null || busy
  const help = settings === null
    ? readError ? null : 'Reading the setting…'
    : settings.enabled
      ? 'On by default. Agents in your sessions may start subsessions.'
      : 'Off. An agent that tries to start a subsession is refused.'

  return (
    <SettingsSection title="Subsessions" id="child-runs" scope="Your account">
      <SettingRow
        label={childRunsLabel}
        labelFor="settings-child-runs"
        help={help}
        control={(
          <Checkbox
            id="settings-child-runs"
            checked={settings?.enabled === true}
            disabled={disabled}
            onCheckedChange={(checked) => change({ enabled: checked === true })}
          />
        )}
      >
        <dl aria-label="What subsessions cover" className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm [&_dt]:text-muted">
          {scope.map(([term, text]) => (
            <Fragment key={term}>
              <dt>{term}</dt>
              <dd className="min-w-0">{text}</dd>
            </Fragment>
          ))}
        </dl>
        {(error ?? readError) && <Callout tone="failed" role="alert">{error ?? readError}</Callout>}
        {readError && settings === null && (
          <TryAgain onClick={() => { useChildSettings.setState({ error: null }); setAttempt((n) => n + 1) }} />
        )}
      </SettingRow>
      {workspace && cap.hasMethod('workspace.child_limits') && <WorkspaceLimits client={client} />}
    </SettingsSection>
  )
}
