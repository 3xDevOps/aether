import { Fragment, type ReactNode, useEffect, useState } from 'react'
import { Confirm } from '@/components/confirm'
import { Callout } from '@/components/ui/callout'
import { Checkbox } from '@/components/ui/checkbox'
import type { Api } from '@/lib/api'
import { errorSentence } from '@/lib/format'
import type { ChildLimits, ChildSettingsParams } from '@/lib/types'
import { SettingRow, SettingsSection, TryAgain } from '@/routes/settings/layout'
import { useStore } from '@/store'
import { saveChildSettings, useChildSettings, useChildSettingsRead } from '@/store/children'
import { useCapability } from '@/store/hooks'

export const childRunsLabel = 'Let my runs start child runs'
export const fallbackLabel = 'Allow automatic fallback'
export const sharedLabel = "Use subscriptions shared with me when I don't have my own"

const fallbackHelp = "When a child's Claude or Codex account has used 95% of its quota, at launch or mid-run, it moves to the account with the most usage left among yours and those shared with you. The agent and model never change."
const sharedHelp = "When you're not signed in to the agent, the child starts on a signed-in account a teammate shared with you."

const scope: [string, string][] = [
  ['Allows', 'An agent in one of your runs starts another agent as a child run, on your account, then messages it and receives its result. You own every child run.'],
  ['Swarms', "Use the swarm's own agents, accounts and fallback. These settings never override them."],
  ['Limits', "The workspace budget and the workspace's child-run limits apply to every child run."],
  ['One run', "Options → Child runs in the launch dialog, or More → Allow child runs on a run, overrides this for that run."],
]

export function limitsText(limits: ChildLimits): string {
  return `${limits.max_live} busy at once, ${limits.max_depth} levels deep, ${limits.max_total} per run tree`
}

function SetupChoices({ fallback, shared, onChange }: {
  fallback: boolean
  shared: boolean
  onChange: (next: { fallback: boolean; shared: boolean }) => void
}) {
  return (
    <div className="flex flex-col gap-3 text-ui">
      {([['fallback', fallbackLabel, fallbackHelp, fallback], ['shared', sharedLabel, sharedHelp, shared]] as const).map(([key, label, help, checked]) => (
        <label key={key} className="flex cursor-pointer items-start gap-2">
          <Checkbox className="mt-0.5" checked={checked} onCheckedChange={(on) => onChange({ fallback, shared, [key]: on === true })} />
          <span className="flex min-w-0 flex-col gap-0.5">
            <span className="font-medium">{label}</span>
            <span className="text-ui-sm text-muted">{help}</span>
          </span>
        </label>
      ))}
    </div>
  )
}

/** The workspace's child-run limits as a sentence: read, reading, or the refusal. */
export function useChildLimitsText(client: Api, workspaceID: string, seq = 0): string {
  const [read, setRead] = useState<{ workspaceID: string; text: string } | null>(null)
  useEffect(() => {
    let live = true
    client.workspaceChildLimits(workspaceID).then(
      (limits) => live && setRead({ workspaceID, text: `${limitsText(limits)}. More child runs wait in a queue.` }),
      (err: unknown) => live && setRead({ workspaceID, text: errorSentence(err) }),
    )
    return () => {
      live = false
    }
  }, [client, workspaceID, seq])
  return read?.workspaceID === workspaceID ? read.text : 'Reading the limits…'
}

export function ChildLimitsRow({ client, workspaceID, seq, control }: { client: Api; workspaceID: string; seq: number; control: ReactNode }) {
  return <SettingRow label="Child runs" help={useChildLimitsText(client, workspaceID, seq)} control={control} />
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
  const [setup, setSetup] = useState<{ fallback: boolean; shared: boolean } | null>(null)

  if (!cap.hasMethod('child.settings')) return null

  // A refused change may still have been stored, so the rows show what the server holds.
  const save = (params: ChildSettingsParams) => saveChildSettings(params, client).catch(async (err: unknown) => {
    await saveChildSettings({}, client).catch(() => {})
    throw err
  })
  const change = (params: ChildSettingsParams) => {
    setBusy(true)
    setError(null)
    save(params).catch((err: unknown) => setError(errorSentence(err))).finally(() => setBusy(false))
  }
  const disabled = settings === null || busy
  const help = settings === null
    ? readError ? null : 'Reading the setting…'
    : settings.enabled
      ? 'On. Agents in your runs may start child runs.'
      : 'Off. An agent that tries to start a child run is refused, and the run shows how to allow it.'

  return (
    <SettingsSection title="Child runs" id="child-runs" scope="Your account">
      <SettingRow
        label={childRunsLabel}
        labelFor="settings-child-runs"
        help={help}
        control={(
          <Checkbox
            id="settings-child-runs"
            checked={settings?.enabled === true}
            disabled={disabled}
            onCheckedChange={(checked) => {
              if (checked === true && settings && !settings.configured) setSetup({ fallback: settings.fallback, shared: settings.shared_when_missing })
              else change({ enabled: checked === true })
            }}
          />
        )}
      >
        <dl aria-label="What child runs cover" className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm [&_dt]:text-muted">
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
      <SettingRow
        id="child-fallback"
        label={fallbackLabel}
        labelFor="settings-child-fallback"
        help={fallbackHelp}
        control={<Checkbox id="settings-child-fallback" checked={settings?.fallback === true} disabled={disabled} onCheckedChange={(on) => change({ fallback: on === true })} />}
      />
      <SettingRow
        id="child-shared"
        label={sharedLabel}
        labelFor="settings-child-shared"
        help={sharedHelp}
        control={<Checkbox id="settings-child-shared" checked={settings?.shared_when_missing === true} disabled={disabled} onCheckedChange={(on) => change({ shared_when_missing: on === true })} />}
      />
      {workspace && cap.hasMethod('workspace.child_limits') && <WorkspaceLimits client={client} />}
      {setup && (
        <Confirm
          title="Let your runs start child runs?"
          description="Agents in your runs will start other agents on your behalf. Choose how child runs pick an account; you can change both here later."
          action="Turn on"
          pending="Turning on…"
          onConfirm={() => save({ enabled: true, fallback: setup.fallback, shared_when_missing: setup.shared })}
          onDone={() => {}}
          onClose={() => setSetup(null)}
        >
          <SetupChoices fallback={setup.fallback} shared={setup.shared} onChange={setSetup} />
        </Confirm>
      )}
    </SettingsSection>
  )
}
